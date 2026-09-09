package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func pauseFactory(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.SetFactoryPause(
		context.Background(), protocol.FactoryPause{Paused: true},
	); err != nil {
		t.Fatal(err)
	}
}

func resumeFactory(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.SetFactoryPause(context.Background(), protocol.FactoryPause{}); err != nil {
		t.Fatal(err)
	}
}

// admitWorkForPauseTest admits one pre-approved orchestrator Work item against
// the shared admission repository, so an eligible worker can actually route it.
func admitWorkForPauseTest(
	t *testing.T, store *Store, requestKey string,
) (protocol.AdmitWorkResponse, error) {
	t.Helper()
	return firstTwo(store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: requestKey, Repository: admissionRepositoryIdentity,
		Name: "Paused work", Spec: "Do the work.", Runtime: string(admissionRuntime),
		Source: protocol.WorkSourceOrchestrator, PreApproved: true,
	}))
}

func TestFactoryPauseBlocksNewAdmission(t *testing.T) {
	store := newTestStore(t)
	pauseFactory(t, store)
	repository := registerTestRepository(t, store, "github.com/example/paused")
	_, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: "pause-00000000-0000-4000-8000-000000000001", Repository: repository.RemoteIdentity,
		Name: "Blocked", Spec: "Do not admit.", Runtime: "claude-code", Source: protocol.WorkSourceOrchestrator,
	})
	if !serviceErrorCode(err, "factory_paused") {
		t.Fatalf("error = %v", err)
	}
}

// TestFactoryPauseBlocksEveryAdmissionRoute names each admission entry point so
// that a new one added without a pause gate fails here rather than silently
// admitting Work while the operator believes Factory is stopped.
func TestFactoryPauseBlocksEveryAdmissionRoute(t *testing.T) {
	t.Run("task run", func(t *testing.T) {
		store := newTestStore(t)
		repository := registerTestRepository(t, store, "github.com/example/task-run")
		taskID := seedTaskForTest(t, store, repository.ID)
		pauseFactory(t, store)
		_, _, err := store.RunTask(context.Background(), taskID,
			protocol.RunTaskRequest{RequestKey: "run-00000000-0000-4000-8000-000000000001"})
		if !serviceErrorCode(err, "factory_paused") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("orchestrator admission", func(t *testing.T) {
		store := newTestStore(t)
		registerTestRepository(t, store, admissionRepositoryIdentity)
		pauseFactory(t, store)
		_, err := admitWorkForPauseTest(t, store, "admit-00000000-0000-4000-8000-000000000001")
		if !serviceErrorCode(err, "factory_paused") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("build", func(t *testing.T) {
		store := newTestStore(t)
		repository := registerTestRepository(t, store, "github.com/example/build")
		pauseFactory(t, store)
		_, err := store.AdmitBuild(context.Background(), protocol.BuildRequest{
			RequestKey:          "build-00000000-0000-4000-8000-000000000001",
			References:          []string{"issue-1"},
			Repository:          repository.RemoteIdentity,
			RepositorySpecified: true,
		})
		if !serviceErrorCode(err, "factory_paused") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("draft approval", func(t *testing.T) {
		store := newTestStore(t)
		registerTestRepository(t, store, admissionRepositoryIdentity)
		response, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
			RequestKey: "draft-00000000-0000-4000-8000-000000000001",
			Repository: admissionRepositoryIdentity, Name: "Draft", Spec: "Await approval.",
			Runtime: string(admissionRuntime), Source: protocol.WorkSourceOrchestrator,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.WorkIDs) == 0 {
			t.Fatal("admission returned no Work to approve")
		}
		pauseFactory(t, store)
		_, err = store.ApproveWork(context.Background(), response.WorkIDs[0],
			protocol.ApproveWorkRequest{Actor: "operator"})
		if !serviceErrorCode(err, "factory_paused") {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestFactoryPauseAllowsRequestKeyReplay is the idempotency guarantee in
// INV-13 and INV-14: a client that already admitted Work and retries the same
// request key must receive its original result, not a refusal, or the CLI's
// durable admission journal is left without an authoritative outcome.
func TestFactoryPauseAllowsRequestKeyReplay(t *testing.T) {
	t.Run("orchestrator admission", func(t *testing.T) {
		store := newTestStore(t)
		registerTestRepository(t, store, admissionRepositoryIdentity)
		const key = "replay-00000000-0000-4000-8000-000000000001"
		admitted, err := admitWorkForPauseTest(t, store, key)
		if err != nil {
			t.Fatal(err)
		}
		pauseFactory(t, store)
		replayed, err := admitWorkForPauseTest(t, store, key)
		if err != nil {
			t.Fatalf("replay while paused failed: %v", err)
		}
		if replayed.RunID != admitted.RunID {
			t.Fatalf("replay returned run %q, want the original %q", replayed.RunID, admitted.RunID)
		}
	})

	t.Run("task run", func(t *testing.T) {
		store := newTestStore(t)
		repository := registerTestRepository(t, store, "github.com/example/replay-task")
		taskID := seedTaskForTest(t, store, repository.ID)
		request := protocol.RunTaskRequest{RequestKey: "replay-00000000-0000-4000-8000-000000000002"}
		admitted, _, err := store.RunTask(context.Background(), taskID, request)
		if err != nil {
			t.Fatal(err)
		}
		pauseFactory(t, store)
		replayed, _, err := store.RunTask(context.Background(), taskID, request)
		if err != nil {
			t.Fatalf("replay while paused failed: %v", err)
		}
		if replayed.Run.ID != admitted.Run.ID {
			t.Fatalf("replay returned run %q, want the original %q", replayed.Run.ID, admitted.Run.ID)
		}
	})
}

// TestFactoryPauseStopsClaimsWithoutFailingWorkers asserts the shape of a
// paused claim as well as its effect. A Worker polling a paused Factory is
// behaving correctly and must be told there is no Work, not handed an error it
// would log as a claim failure on every poll.
func TestFactoryPauseStopsClaimsWithoutFailingWorkers(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, admissionRepositoryIdentity)
	worker := eligibleWorkerForAdmission(t, store, workerA)
	if _, err := admitWorkForPauseTest(t, store, "claim-00000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	pauseFactory(t, store)
	claim, err := store.Claim(context.Background(), worker.ID,
		protocol.ClaimRequest{RequestID: "22222222-2222-4222-8222-222222222222", LeaseToken: tokenA})
	if err != nil {
		t.Fatalf("a paused claim returned an error rather than no Work: %v", err)
	}
	if claim != nil {
		t.Fatalf("a paused Factory dispatched attempt %s", claim.Attempt.ID)
	}
}

// TestFactoryResumeMakesQueuedWorkClaimable is the other half: the pause must
// be the only thing holding the Work, so resuming has to release it without
// any further operator action.
func TestFactoryResumeMakesQueuedWorkClaimable(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, admissionRepositoryIdentity)
	worker := eligibleWorkerForAdmission(t, store, workerA)
	if _, err := admitWorkForPauseTest(t, store, "resume-00000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	pauseFactory(t, store)
	if claim, err := store.Claim(context.Background(), worker.ID,
		protocol.ClaimRequest{RequestID: "22222222-2222-4222-8222-222222222222", LeaseToken: tokenA},
	); err != nil || claim != nil {
		t.Fatalf("claim while paused = %#v, %v", claim, err)
	}
	resumeFactory(t, store)
	claim, err := store.Claim(context.Background(), worker.ID,
		protocol.ClaimRequest{RequestID: "33333333-3333-4333-8333-333333333333", LeaseToken: tokenA})
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatal("resuming did not make eligible queued Work claimable")
	}
}

// TestResumeWakesEveryWaitingLoop covers the broadcast, not just the signal.
// The scheduler and the fake-cloud dispatcher both wait in waitForWork, so a
// signal that only one of them can consume would leave the other asleep for
// the rest of its poll interval.
func TestResumeWakesEveryWaitingLoop(t *testing.T) {
	store := newTestStore(t)
	const waiters = 3
	woke := make(chan bool, waiters)
	ready := make(chan struct{}, waiters)
	for index := 0; index < waiters; index++ {
		go func() {
			// resumeSignal is taken before the wait, exactly as waitForWork
			// does, so the resume below cannot land in between.
			resumed := store.resumeSignal()
			ready <- struct{}{}
			select {
			case <-resumed:
				woke <- true
			case <-time.After(10 * time.Second):
				woke <- false
			}
		}()
	}
	for index := 0; index < waiters; index++ {
		<-ready
	}
	resumeFactory(t, store)
	for index := 0; index < waiters; index++ {
		if !<-woke {
			t.Fatal("a waiting loop was not woken by resume")
		}
	}
}

// TestWaitForWorkReturnsOnContextCancellation keeps the loops shutdown-safe:
// every one of them exits by way of this function.
func TestWaitForWorkReturnsOnContextCancellation(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if store.waitForWork(ctx, time.Hour) {
		t.Fatal("waitForWork reported more work after its context ended")
	}
}

func TestFactoryPauseRoundTripsItsTimestamp(t *testing.T) {
	store := newTestStore(t)
	paused, err := store.SetFactoryPause(context.Background(), protocol.FactoryPause{Paused: true})
	if err != nil {
		t.Fatal(err)
	}
	if paused.PausedAt == nil {
		t.Fatal("pausing recorded no timestamp")
	}
	stored, err := store.FactoryPause(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Paused || stored.PausedAt == nil {
		t.Fatalf("stored pause = %+v", stored)
	}
	// Resuming clears the timestamp as well as the flag: a stale "paused 2h
	// ago" beside a running Factory reads as though it were still stopped.
	resumed, err := store.SetFactoryPause(context.Background(), protocol.FactoryPause{})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Paused || resumed.PausedAt != nil {
		t.Fatalf("resumed pause = %+v", resumed)
	}
	if stored, err = store.FactoryPause(context.Background()); err != nil || stored.Paused ||
		stored.PausedAt != nil {
		t.Fatalf("stored pause after resume = %+v, %v", stored, err)
	}
}
