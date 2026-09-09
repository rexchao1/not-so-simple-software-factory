package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/owainlewis/factory/internal/protocol"
)

// maybeAutoMerge merges a delivered pull request only after project opt-in,
// server-side delivery verification, and passing GitHub checks. Reviewed work
// additionally requires an independent approval; explicit fast assurance is
// the narrow exception. This is the only writer of a work_updates row with
// actor = system.
//
// A merge is a POST-TERMINAL event. By the time this runs, the ready outcome is
// already durable and the Work is already in its terminal state, and nothing
// here changes that. On success it adds a ledger row; on refusal it records the
// refusal on the Work and stops. Either way the delivery stands.
//
// It returns an error only for a fault that made the decision impossible to
// take, never for a decision not to merge and never for a merge GitHub refused.
// The ready outcome is correct whatever happens here, so failing this must not
// fail the Work.
func maybeAutoMerge(ctx context.Context, store *Store, workID string) error {
	var delivery, pullRequestURL, headSHA, verificationSource, assurance string
	err := store.db.QueryRowContext(ctx, `
		SELECT session.delivery, session.pull_request_url, session.pull_request_head_sha,
		       session.delivery_verification_source, run.assurance
		FROM sessions session JOIN runs run ON run.id = session.run_id
		WHERE session.id = ?
	`, workID).Scan(&delivery, &pullRequestURL, &headSHA, &verificationSource, &assurance)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}

	// Condition 1, the project opted in. This is a local read and it comes
	// first on purpose: a project with auto-merge off must cost no GitHub call
	// at all. TestAutoMergeOffMakesNoGitHubCallAtAll asserts exactly that.
	if protocol.DeliveryMode(delivery) != protocol.DeliveryPullRequestAutoMerge {
		return nil
	}

	// The server must have verified this delivery itself. A ready recorded
	// before migration 039, or by a server with no GitHub credential, carries
	// an empty source, and merging it would mean trusting the agent's word for
	// what INV-3 exists to check. Fail closed.
	if verificationSource != "server-github" {
		return nil
	}

	// Reviewed work still requires an independent approval. Fast assurance is
	// an explicit orchestrator decision to trade that review for latency and
	// usage; it may merge after the same server-side delivery and GitHub check
	// verification as reviewed work.
	if protocol.AssuranceMode(assurance) != protocol.AssuranceFast {
		verdict, err := store.ReviewVerdict(ctx, workID)
		if err != nil {
			return err
		}
		if verdict != protocol.ReviewVerdictApprove {
			return nil
		}
	}

	if store.github == nil || pullRequestURL == "" || headSHA == "" {
		return nil
	}

	// Nothing merges twice. The outcome path can run again for one Work after
	// a retried forward or a resumed worker, and a second attempt on a merged
	// pull request is a 405 an operator then has to interpret.
	var alreadyMerged int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM work_updates WHERE work_id = ? AND status = 'merged'
	`, workID).Scan(&alreadyMerged); err != nil {
		return unavailable(err)
	}
	if alreadyMerged > 0 {
		return nil
	}

	owner, repository, number, err := parsePullRequestURL(pullRequestURL)
	if err != nil {
		return nil
	}

	// Condition 3, no check is failing. This is the network read, so it runs
	// last: the two local conditions have already excluded everything they can.
	//
	// Read what "no check is failing" can and cannot mean here. On a private
	// repository with no workflows and no branch protection, there are no
	// checks at all, and zero checks therefore passes. That is the honest
	// reading of INV-8 as written, which says checks pass, not checks exist,
	// and it becomes meaningful the moment a workflow exists. A read that
	// fails is treated as failing, not passing.
	failing, err := store.github.FailingChecks(ctx, owner, repository, headSHA)
	if err != nil {
		recordMergeRefusal(ctx, store, workID, fmt.Sprintf("checks could not be read: %v", err))
		return nil
	}
	if len(failing) > 0 {
		return nil
	}

	// The SHA is pinned to the one the server verified. GitHub refuses with 409
	// if the branch moved in between, so a race cannot merge unverified code.
	if err := store.github.MergePullRequest(ctx, owner, repository, number, headSHA); err != nil {
		// Recorded, and not retried. A refusal usually means a human needs to
		// look, and a silent retry loop against GitHub is the wrong answer to
		// every reason a merge is refused.
		recordMergeRefusal(ctx, store, workID, err.Error())
		return nil
	}

	return recordMerge(ctx, store, workID, pullRequestURL, headSHA, number)
}

// recordMerge writes the ledger row. It is the first and only writer of
// actor = 'system': a human merge produces no row at all, because the human
// acts on GitHub and factory is never told, so this actor is unambiguous
// evidence that factory did it.
func recordMerge(
	ctx context.Context,
	store *Store,
	workID, pullRequestURL, headSHA string,
	number int,
) error {
	updateID, err := newID()
	if err != nil {
		return unavailable(err)
	}
	requestID, err := newID()
	if err != nil {
		return unavailable(err)
	}
	now := store.now().UnixMilli()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()

	// The merge row rides alongside the attempt's ready row rather than
	// replacing it, which is why migration 039 exempts 'merged' from
	// work_updates_attempt_outcome. attempt_id is carried over from the ready
	// row so the two stay associated.
	var attemptID sql.NullString
	var sequence int
	if err := tx.QueryRowContext(ctx, `
		SELECT attempt_id FROM work_updates
		WHERE work_id = ? AND status = 'ready' ORDER BY sequence DESC LIMIT 1
	`, workID).Scan(&attemptID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return unavailable(err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM work_updates WHERE work_id = ?
	`, workID).Scan(&sequence); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_updates(
			id, work_id, attempt_id, request_id, sequence, status, message,
			pull_request_url, pull_request_head_sha, actor, accepted_at
		) VALUES (?, ?, ?, ?, ?, 'merged', ?, ?, ?, 'system', ?)
	`, updateID, workID, attemptID, requestID, sequence,
		fmt.Sprintf("Factory merged pull request #%d at %s.", number, headSHA),
		pullRequestURL, headSHA, now); err != nil {
		return unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	return nil
}

// recordMergeRefusal puts the reason where an operator will see it. A refused
// merge is not a failed Work, so the state is untouched and only the terminal
// message is appended to.
func recordMergeRefusal(ctx context.Context, store *Store, workID, reason string) {
	message := "Automatic merge was refused: " + reason
	if _, err := store.db.ExecContext(ctx, `
		UPDATE sessions
		SET terminal_message = substr(
			CASE WHEN terminal_message = '' THEN ? ELSE terminal_message || char(10) || ? END,
			1, 8192)
		WHERE id = ?
	`, message, message, workID); err != nil {
		// Nothing useful is left to do. The merge already did not happen, the
		// delivery still stands, and failing the Work over a lost note would
		// be worse than the lost note.
		return
	}
}
