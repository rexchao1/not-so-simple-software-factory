package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// ApproveWork is the single human gate described by INV-10. It moves one
// draft session to queued, or to blocked when no worker can take it yet, and
// records who approved it. Approval mirrors AnswerWork: reselect a worker in
// the same transaction and requeue. It refuses any source state other than
// draft, so approving twice is a conflict rather than a silent no-op.
func (s *Store) ApproveWork(
	ctx context.Context, workID string, input protocol.ApproveWorkRequest,
) (protocol.Work, error) {
	actor := strings.TrimSpace(input.Actor)
	if actor == "" || len(actor) > 255 {
		return protocol.Work{}, invalid(
			"invalid_actor", "actor is required and limited to 255 bytes",
		)
	}

	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Work{}, unavailable(err)
	}
	defer func() { _ = tx.Rollback() }()

	var runID, state, repositoryID, identity, runtime string
	err = tx.QueryRowContext(ctx, `
		SELECT run_id, state, repository_id, repository_identity, required_runtime
		FROM sessions WHERE id = ?
	`, workID).Scan(&runID, &state, &repositoryID, &identity, &runtime)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Work{}, &ServiceError{
			Code: "work_not_found", Message: "no Work matches that identifier", Status: 404,
		}
	}
	if err != nil {
		return protocol.Work{}, unavailable(err)
	}
	if state != string(protocol.SessionDraft) {
		return protocol.Work{}, conflict("work_not_draft", "only draft Work can be approved")
	}
	// Approval is an admission decision: it releases a draft into the queue.
	// Checked after the draft lookup so a paused Factory still reports the
	// more specific work_not_found or work_not_draft when either applies.
	if err := pauseGate(ctx, tx, pauseAdmissionMessage); err != nil {
		return protocol.Work{}, err
	}

	var concurrencySlotAvailable int
	if err := tx.QueryRowContext(ctx, `
		SELECT (
			SELECT COUNT(*) FROM sessions active
			WHERE active.run_id = run.id AND active.state IN ('queued', 'preparing', 'running')
		) < json_extract(run.task_snapshot, '$.concurrency_limit')
		FROM runs run WHERE run.id = ?
	`, runID).Scan(&concurrencySlotAvailable); err != nil {
		return protocol.Work{}, unavailable(err)
	}
	assignedWorkerID, blockedReason := "", taskConcurrencyBlockedReason
	if concurrencySlotAvailable != 0 {
		assignedWorkerID, blockedReason, err = s.resumeRoute(ctx, tx, repositoryID, identity, runtime, now)
		if err != nil {
			return protocol.Work{}, err
		}
	}
	if assignedWorkerID != "" {
		if err := queueExistingExecution(ctx, tx, workID, assignedWorkerID, runtime, now); err != nil {
			return protocol.Work{}, err
		}
	}
	nextState := "queued"
	if assignedWorkerID == "" {
		nextState = "blocked"
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions
		SET state = ?, blocked_reason = ?, assigned_worker_id = ?, waiting_reason = ?,
		    approved_by = ?, approved_at = ?
		WHERE id = ? AND state = 'draft'
	`, nextState, nullableString(blockedReason), nullableString(assignedWorkerID),
		boundedUTF8Bytes(blockedReason, protocol.MaxWaitingReasonBytes),
		actor, now, workID); err != nil {
		return protocol.Work{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET updated_at = ?, terminal_at = NULL WHERE id = ?
	`, now, runID); err != nil {
		return protocol.Work{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.Work{}, unavailable(err)
	}
	return s.Work(ctx, workID)
}
