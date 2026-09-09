package controlplane

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// seedReadyWork drives a two stage Work all the way to a verified ready
// outcome, with the delivery mode and review verdict a case needs, and returns
// the store, the GitHub it was verified against, and the Work.
func seedReadyWork(
	t *testing.T,
	delivery protocol.DeliveryMode,
	verdict protocol.ReviewVerdict,
	failing []string,
) (*Store, *fakeGitHub, twoStageWork) {
	t.Helper()
	return seedReadyWorkRefusing(t, delivery, verdict, failing, nil)
}

func seedReadyWorkRefusing(
	t *testing.T,
	delivery protocol.DeliveryMode,
	verdict protocol.ReviewVerdict,
	failing []string,
	mergeErr error,
) (*Store, *fakeGitHub, twoStageWork) {
	t.Helper()
	return seedReadyWorkWithAssurance(t, delivery, verdict, failing, mergeErr, protocol.AssuranceReviewed)
}

func seedReadyWorkWithAssurance(
	t *testing.T,
	delivery protocol.DeliveryMode,
	verdict protocol.ReviewVerdict,
	failing []string,
	mergeErr error,
	assurance protocol.AssuranceMode,
) (*Store, *fakeGitHub, twoStageWork) {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	work := seedTwoStageWork(t, store)
	if _, err := store.db.ExecContext(ctx, `UPDATE runs SET assurance = ? WHERE id = (SELECT run_id FROM sessions WHERE id = ?)`, assurance, work.id); err != nil {
		t.Fatal(err)
	}

	ready := protocol.AttemptUpdateRequest{
		LeaseToken: work.leaseToken, RequestID: "24000000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateReady, Message: "Pull request is ready.",
		PullRequestURL:        testReadyPullRequest,
		PullRequestHeadBranch: work.branch,
		PullRequestHeadSHA:    testReadyHeadSHA,
	}
	agreeWithReadyEvidence(t, store, ready)
	fake := store.github.(*fakeGitHub)
	// The pull request is on the Work's own repository, which is what INV-3
	// clause one compares against.
	fake.pullRequests["octocat/factory-scratch#2"] = fakePullRequest{
		HeadRepo: "owainlewis/factory", HeadRef: work.branch,
		HeadSHA: testReadyHeadSHA, State: "open",
	}
	if len(failing) > 0 {
		fake.failing["octocat/factory-scratch@"+testReadyHeadSHA] = failing
	}

	succeedImplementingStage(t, store, work)
	if _, err := store.CompleteStage(ctx, work.attemptID, 1, protocol.CompleteStageRequest{
		LeaseToken: work.leaseToken, State: protocol.StageSucceeded, ReviewVerdict: verdict,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAgentUpdate(ctx, work.attemptID, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE sessions SET delivery = ? WHERE id = ?`, delivery, work.id); err != nil {
		t.Fatal(err)
	}
	// Only the calls the merge decision makes should count, so the ones
	// INV-3 verification already made are cleared first.
	fake.calls = nil
	fake.mergeErr = mergeErr

	// Completing the Attempt is what makes the Work terminal and puts the
	// delivery evidence on the session, and it is where the merge is decided.
	// The delivery mode is set first so this call sees it.
	if _, err := store.CompleteAttempt(ctx, work.attemptID, protocol.CompleteAttemptRequest{
		LeaseToken: work.leaseToken, State: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	return store, fake, work
}

func TestFastAssuranceMayAutoMergeWithoutAReviewVerdict(t *testing.T) {
	store, fake, work := seedReadyWorkWithAssurance(t, protocol.DeliveryPullRequestAutoMerge,
		protocol.ReviewVerdictNone, nil, nil, protocol.AssuranceFast)
	if fake.merges != 1 {
		t.Fatalf("fast verified delivery merges = %d, want 1; calls: %v", fake.merges, fake.calls)
	}
	detail, err := store.Run(t.Context(), work.runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Sessions) != 1 || !slices.ContainsFunc(detail.Sessions[0].Updates,
		func(update protocol.WorkUpdate) bool { return update.Status == protocol.WorkUpdateMerged }) {
		t.Fatalf("Run detail does not expose the merge ledger: %#v", detail.Sessions)
	}
}

func workUpdateStatuses(t *testing.T, store *Store, workID string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(),
		`SELECT status FROM work_updates WHERE work_id = ? ORDER BY sequence`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var statuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return statuses
}

func workUpdateActor(t *testing.T, store *Store, workID, status string) string {
	t.Helper()
	var actor string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT actor FROM work_updates WHERE work_id = ? AND status = ?`, workID, status).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	return actor
}

func sessionTerminalMessage(t *testing.T, store *Store, workID string) string {
	t.Helper()
	var message string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT terminal_message FROM sessions WHERE id = ?`, workID).Scan(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestINV8MergesOnlyWhenAllThreeConditionsHold(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		delivery  protocol.DeliveryMode
		verdict   protocol.ReviewVerdict
		failing   []string
		wantMerge bool
	}{
		{name: "all three", delivery: protocol.DeliveryPullRequestAutoMerge, verdict: protocol.ReviewVerdictApprove, wantMerge: true},

		// Each of the three, alone, withheld. Two of three must never merge.
		{name: "auto-merge off", delivery: protocol.DeliveryPullRequest, verdict: protocol.ReviewVerdictApprove},
		{name: "checks failing", delivery: protocol.DeliveryPullRequestAutoMerge, verdict: protocol.ReviewVerdictApprove, failing: []string{"build (failure)"}},
		{name: "no verdict", delivery: protocol.DeliveryPullRequestAutoMerge, verdict: protocol.ReviewVerdictNone},

		{name: "verdict request-changes", delivery: protocol.DeliveryPullRequestAutoMerge, verdict: protocol.ReviewVerdictRequestChanges},
		{name: "verdict blocked", delivery: protocol.DeliveryPullRequestAutoMerge, verdict: protocol.ReviewVerdictBlocked},

		// A check still running is not a check that passed.
		{name: "checks still running", delivery: protocol.DeliveryPullRequestAutoMerge, verdict: protocol.ReviewVerdictApprove, failing: []string{"build (still running)"}},

		// Branch delivery has no pull request to merge.
		{name: "branch delivery", delivery: protocol.DeliveryBranch, verdict: protocol.ReviewVerdictApprove},

		// Nothing configured at all: the vacuous case, which does merge. This
		// is the honest reading of "checks pass" on a repository with no
		// checks, and it is asserted so the behaviour is deliberate rather
		// than incidental.
		{name: "no checks exist", delivery: protocol.DeliveryPullRequestAutoMerge, verdict: protocol.ReviewVerdictApprove, wantMerge: true},

		{name: "none of the three", delivery: protocol.DeliveryPullRequest, verdict: protocol.ReviewVerdictNone, failing: []string{"build (failure)"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// The seed drives the real path: CompleteAttempt is what decides
			// the merge, so this asserts the wiring and not just the function.
			store, fake, work := seedReadyWork(t, testCase.delivery, testCase.verdict, testCase.failing)

			if testCase.wantMerge && fake.merges != 1 {
				t.Fatalf("merges = %d, want 1. calls: %v", fake.merges, fake.calls)
			}
			if !testCase.wantMerge && fake.merges != 0 {
				t.Fatalf("MERGED WITHOUT ALL THREE CONDITIONS. merges = %d, calls: %v", fake.merges, fake.calls)
			}

			ledger := workUpdateStatuses(t, store, work.id)
			if testCase.wantMerge && !slices.Contains(ledger, "merged") {
				t.Fatalf("a merge was not recorded in the ledger: %v", ledger)
			}
			if !testCase.wantMerge && slices.Contains(ledger, "merged") {
				t.Fatalf("a merge was recorded but none happened: %v", ledger)
			}
		})
	}
}

func TestAutoMergeOffMakesNoGitHubCallAtAll(t *testing.T) {
	// Order matters for cost. The delivery mode and the verdict are local
	// reads, so a project with auto-merge off must be decided without asking
	// GitHub anything.
	_, fake, _ := seedReadyWork(t, protocol.DeliveryPullRequest, protocol.ReviewVerdictApprove, nil)
	if len(fake.calls) != 0 {
		t.Fatalf("a project with auto-merge off still called GitHub: %v", fake.calls)
	}
}

func TestMergeIsRecordedAsSystemNotAgent(t *testing.T) {
	// A human merge produces no work_updates row at all, because the human acts
	// on GitHub and factory is never told. So actor = 'system' is unambiguous
	// evidence that factory merged. This is the first row in the codebase's
	// history to use that actor.
	store, _, work := seedReadyWork(t, protocol.DeliveryPullRequestAutoMerge, protocol.ReviewVerdictApprove, nil)
	actor := workUpdateActor(t, store, work.id, "merged")
	if actor != string(protocol.WorkUpdateActorSystem) {
		t.Fatalf("merge actor = %q, want system", actor)
	}
}

func TestARefusedMergeIsRecordedAndNotRetried(t *testing.T) {
	// The refusal is injected before the completion that decides the merge,
	// and CompleteAttempt must still succeed: a merge factory could not do
	// does not make a delivered Work undelivered.
	store, fake, work := seedReadyWorkRefusing(t, protocol.DeliveryPullRequestAutoMerge,
		protocol.ReviewVerdictApprove, nil,
		errors.New("github PUT /merge: 405 Pull Request is not mergeable"))

	// It must be visible.
	message := sessionTerminalMessage(t, store, work.id)
	if !strings.Contains(message, "not mergeable") {
		t.Fatalf("the refusal was not recorded: %q", message)
	}
	// And it must not have been retried.
	if attempts := fake.mergeAttempts(); attempts != 1 {
		t.Fatalf("merge attempted %d times, want 1. A refused merge is not retried silently.", attempts)
	}
	// A refused merge leaves no merged row: the ledger records what happened,
	// not what was tried.
	if ledger := workUpdateStatuses(t, store, work.id); slices.Contains(ledger, "merged") {
		t.Fatalf("a refused merge was recorded as merged: %v", ledger)
	}
}

func TestTheMergeUsesTheVerifiedSHA(t *testing.T) {
	// Passing sha makes GitHub refuse with 409 if the branch moved between
	// verification and merge. Without it, a race merges code nobody verified.
	_, fake, _ := seedReadyWork(t, protocol.DeliveryPullRequestAutoMerge, protocol.ReviewVerdictApprove, nil)
	var merge string
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "MergePullRequest") {
			merge = call
		}
	}
	if !strings.Contains(merge, "sha="+testReadyHeadSHA) {
		t.Fatalf("merge did not pin the verified head SHA: %q", merge)
	}
}

func TestAMergeIsNotAttemptedTwice(t *testing.T) {
	// The outcome path can run more than once for one Work: a retried forward,
	// a resumed worker. A second merge attempt on an already merged pull
	// request is a 405 the operator then has to interpret.
	store, fake, work := seedReadyWork(t, protocol.DeliveryPullRequestAutoMerge, protocol.ReviewVerdictApprove, nil)
	// The completion already merged. A second decision on the same Work must
	// do nothing at all.
	if err := maybeAutoMerge(t.Context(), store, work.id); err != nil {
		t.Fatal(err)
	}
	if fake.mergeAttempts() != 1 {
		t.Fatalf("merge attempted %d times, want 1: %v", fake.mergeAttempts(), fake.calls)
	}
	ledger := workUpdateStatuses(t, store, work.id)
	merged := 0
	for _, status := range ledger {
		if status == "merged" {
			merged++
		}
	}
	if merged != 1 {
		t.Fatalf("the ledger holds %d merge rows, want 1: %v", merged, ledger)
	}
}
