package controlplane

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

type twoStageWork struct {
	id         string
	attemptID  string
	leaseToken string
	runID      string
	branch     string
}

// seedTwoStageWork builds the shape INV-8's verdict condition depends on: an
// implementing stage at position 0 and a reviewing stage at position 1, both
// claimed by one Attempt. CompleteStage is keyed by attempt, and the verdict is
// read back by Work, so both ids come back.
func seedTwoStageWork(t *testing.T, store *Store) twoStageWork {
	t.Helper()
	ctx := context.Background()
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	pipeline, err := store.CreatePipeline(ctx, protocol.SavePipelineRequest{
		Name: "Implement then review",
		Stages: []protocol.PipelineStage{
			{Name: "Implement", Prompt: "Implement {{ task.prompt }}"},
			{Name: "Review", Prompt: "Review the implementation you did not write, and record a verdict."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Reviewed work", Prompt: "the requested behavior", Runtime: protocol.RuntimeCodex,
		PipelineID: pipeline.ID, RepositoryIDs: []string{worker.Repositories[0].ID},
		OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "review-verdict"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{
		RequestID: "review-verdict-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	// Only stage 0 starts here. StartStage refuses position 1 until its
	// predecessor has succeeded, which is the ordering that makes the reviewer
	// a distinct process from the implementer in the first place.
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	return twoStageWork{
		id: run.Sessions[0].ID, attemptID: claim.Attempt.ID, leaseToken: tokenA,
		runID: run.Run.ID, branch: run.Sessions[0].Target.PublishBranch,
	}
}

// succeedImplementingStage completes stage 0 with no verdict and starts the
// reviewing stage, which is the only order the Pipeline permits.
func succeedImplementingStage(t *testing.T, store *Store, work twoStageWork) {
	t.Helper()
	if _, err := store.CompleteStage(t.Context(), work.attemptID, 0, protocol.CompleteStageRequest{
		LeaseToken: work.leaseToken, State: protocol.StageSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStage(t.Context(), work.attemptID, 1, protocol.StartStageRequest{
		LeaseToken: work.leaseToken,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewVerdictComesFromAStageThatDidNotImplement(t *testing.T) {
	// INV-8's third condition is only worth anything if the verdict was
	// recorded by a different process from the one that wrote the code. A
	// verdict on position 0 of a single-stage pipeline is self-approval, and
	// must not count.
	store := newTestStore(t)
	work := seedTwoStageWork(t, store)

	// Stage 0, the implementer, tries to approve its own work.
	if _, err := store.CompleteStage(t.Context(), work.attemptID, 0, protocol.CompleteStageRequest{
		LeaseToken:    work.leaseToken,
		State:         protocol.StageSucceeded,
		ReviewVerdict: protocol.ReviewVerdictApprove,
	}); err == nil {
		t.Fatal("the implementing stage was allowed to record a verdict")
	}

	succeedImplementingStage(t, store, work)
	if _, err := store.CompleteStage(t.Context(), work.attemptID, 1, protocol.CompleteStageRequest{
		LeaseToken:    work.leaseToken,
		State:         protocol.StageSucceeded,
		ReviewVerdict: protocol.ReviewVerdictApprove,
	}); err != nil {
		t.Fatal(err)
	}

	verdict, err := store.ReviewVerdict(t.Context(), work.id)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != protocol.ReviewVerdictApprove {
		t.Fatalf("verdict = %q, want approve", verdict)
	}
}

func TestReviewVerdictDefaultsToNone(t *testing.T) {
	// A pipeline with no reviewing stage records no verdict, and INV-8 must
	// treat that as "do not merge". This is the failure direction a silent
	// prompt regression takes, and it is the safe one.
	store := newTestStore(t)
	work := seedTwoStageWork(t, store)
	verdict, err := store.ReviewVerdict(t.Context(), work.id)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != protocol.ReviewVerdictNone {
		t.Fatalf("verdict = %q, want the empty verdict", verdict)
	}
}

func TestReviewVerdictRejectsAnUnknownValue(t *testing.T) {
	store := newTestStore(t)
	work := seedTwoStageWork(t, store)
	succeedImplementingStage(t, store, work)
	if _, err := store.CompleteStage(t.Context(), work.attemptID, 1, protocol.CompleteStageRequest{
		LeaseToken:    work.leaseToken,
		State:         protocol.StageSucceeded,
		ReviewVerdict: protocol.ReviewVerdict("lgtm"),
	}); !serviceErrorCode(err, "invalid_review_verdict") {
		t.Fatalf("an unknown verdict was accepted, error = %v", err)
	}
}

func TestReviewVerdictIsTheHighestStageThatRecordedOne(t *testing.T) {
	// A later reviewing stage overrides an earlier one. Blocking last must
	// block, or a pipeline could approve, then find a problem, and still merge.
	store := newTestStore(t)
	work := seedTwoStageWork(t, store)
	succeedImplementingStage(t, store, work)
	if _, err := store.CompleteStage(t.Context(), work.attemptID, 1, protocol.CompleteStageRequest{
		LeaseToken: work.leaseToken, State: protocol.StageSucceeded,
		ReviewVerdict: protocol.ReviewVerdictBlocked,
	}); err != nil {
		t.Fatal(err)
	}
	verdict, err := store.ReviewVerdict(t.Context(), work.id)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != protocol.ReviewVerdictBlocked {
		t.Fatalf("verdict = %q, want blocked", verdict)
	}
}

func TestReviewVerdictSurvivesOnTheStageRecord(t *testing.T) {
	// stageRun did not select review_verdict when the column was added, and a
	// verdict that is written but never read back is indistinguishable from one
	// that was never recorded.
	store := newTestStore(t)
	work := seedTwoStageWork(t, store)
	succeedImplementingStage(t, store, work)
	stage, err := store.CompleteStage(t.Context(), work.attemptID, 1, protocol.CompleteStageRequest{
		LeaseToken: work.leaseToken, State: protocol.StageSucceeded,
		ReviewVerdict: protocol.ReviewVerdictRequestChanges,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stage.ReviewVerdict != protocol.ReviewVerdictRequestChanges {
		t.Fatalf("the completed stage reported verdict %q, want request-changes", stage.ReviewVerdict)
	}
}
