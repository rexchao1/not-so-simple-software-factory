package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

// criticFindings is what the critic actually returns: JSON and nothing else.
// Factory stores it whole and never parses it, which is why the tests below
// compare it byte for byte rather than reading a field out of it.
const criticFindings = `{"round":1,"verdict":"revise","findings":[` +
	`{"id":"F1","severity":"blocking","kind":"undecided","where":"D3",` +
	`"claim":"D3 cites a config that says something else.",` +
	`"evidence":"config/auth.ts:41","fix":"Cite the config or mark it Not yet specified."}]}`

// criticPassClock is a clock these tests move by hand, so a pass has a
// duration and a cost that were measured rather than raced. It stays well
// inside the lease so a completion is never refused for a lost lease.
type criticPassClock struct {
	at time.Time
}

func (c *criticPassClock) now() time.Time { return c.at }

// submitCritiquePass admits one critique Work item against a checkpoint and
// leaves it queued, which is what a submitted-and-not-yet-claimed pass is.
func submitCritiquePass(
	t *testing.T, store *Store, requestKey string, number, round int,
) protocol.AdmitWorkResponse {
	t.Helper()
	response, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: requestKey,
		Planning:   &protocol.WorkPlanning{Project: "payer", Number: number, Round: round},
	})
	if err != nil {
		t.Fatalf("submit a critique pass: %v", err)
	}
	return response
}

// runCritiquePass takes one pass all the way through: claimed, started, and
// completed with the findings and the cost the runtime reported. It returns
// the Work id so a caller can read the result back.
func runCritiquePass(
	t *testing.T, store *Store, clock *criticPassClock, worker protocol.Worker,
	requestKey string, number, round int, cost float64, duration time.Duration,
) string {
	t.Helper()
	ctx := context.Background()
	// The clock these tests hold still is the same clock a Worker's heartbeat
	// ages against, so a pass admitted minutes of test time after the Worker
	// registered would find nothing online. One heartbeat, at the moment of
	// submission, keeps the Worker as present as a real one would be.
	if _, err := store.HeartbeatWorker(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}
	response := submitCritiquePass(t, store, requestKey, number, round)
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{
		RequestID: requestKey, LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{
		LeaseToken: tokenA,
	}); err != nil {
		t.Fatal(err)
	}
	clock.at = clock.at.Add(duration)
	if _, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: criticFindings, CostUSD: &cost,
	}); err != nil {
		t.Fatal(err)
	}
	return response.WorkIDs[0]
}

// criticPassFixture is the roadmap fixture plus a worker that can run a
// critique, on a clock the test controls.
func criticPassFixture(t *testing.T, store *Store) (*criticPassClock, protocol.Worker) {
	t.Helper()
	clock := &criticPassClock{at: time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)}
	store.now = clock.now
	seedRoadmapFixture(t, store)
	return clock, eligibleWorkerForAdmission(t, store, workerA)
}

// This is what the orchestrator's ledger.tsv used to say, read from the Work
// row that actually ran instead.
func TestRoadmapReadsAFinishedCritiqueAsAPass(t *testing.T) {
	store := newTestStore(t)
	clock, worker := criticPassFixture(t, store)
	runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000001", 2, 1, 0.42, 20*time.Second)

	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if len(checkpoint.Passes) != 1 {
		t.Fatalf("passes = %+v, want one", checkpoint.Passes)
	}
	pass := checkpoint.Passes[0]
	if pass.Mode != "critique" || pass.Round != 1 {
		t.Errorf("mode and round = %q %d, want critique 1", pass.Mode, pass.Round)
	}
	if pass.Outcome != string(protocol.SessionSucceeded) {
		t.Errorf("outcome = %q, want the Work state succeeded", pass.Outcome)
	}
	if pass.CostUSD != 0.42 {
		t.Errorf("cost = %v, want the summed attempt cost 0.42", pass.CostUSD)
	}
	if pass.DurationMS != 20000 {
		t.Errorf("duration = %dms, want 20000 measured from start to terminal", pass.DurationMS)
	}
	if pass.Model == "" {
		t.Error("the pass reports no model; it comes from the frozen execution")
	}
	if pass.At.IsZero() {
		t.Error("a finished pass has no time on it")
	}
	if checkpoint.Live != nil {
		t.Errorf("a finished pass is still reported as live: %+v", checkpoint.Live)
	}
}

// Cost and rounds roll up to the checkpoint and from there to the project,
// because that is the number the human reads to decide whether another round
// is worth it.
func TestRoadmapRollsPassesUpToCheckpointAndProject(t *testing.T) {
	store := newTestStore(t)
	clock, worker := criticPassFixture(t, store)
	runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000002", 2, 1, 0.40, 10*time.Second)
	clock.at = clock.at.Add(time.Minute)
	runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000003", 2, 2, 0.60, 10*time.Second)
	clock.at = clock.at.Add(time.Minute)
	runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000004", 1, 1, 0.25, 10*time.Second)

	roadmap := readRoadmapForTest(t, store)
	project := roadmap.Projects[0]
	if project.CostUSD < 1.2499 || project.CostUSD > 1.2501 {
		t.Errorf("project cost = %v, want 1.25 summed over its checkpoints", project.CostUSD)
	}
	second := roadmapFixtureCheckpoint(t, store, 2)
	if second.PassRounds != 2 {
		t.Errorf("checkpoint 2 rounds = %d, want 2", second.PassRounds)
	}
	if second.CostUSD < 0.9999 || second.CostUSD > 1.0001 {
		t.Errorf("checkpoint 2 cost = %v, want 1.00", second.CostUSD)
	}
	if len(second.Passes) != 2 || second.Passes[0].Round != 1 || second.Passes[1].Round != 2 {
		t.Errorf("checkpoint 2 passes are not in round order: %+v", second.Passes)
	}
	first := roadmapFixtureCheckpoint(t, store, 1)
	if first.PassRounds != 1 || first.CostUSD != 0.25 {
		t.Errorf("checkpoint 1 = %d rounds at %v, want 1 at 0.25", first.PassRounds, first.CostUSD)
	}
	third := roadmapFixtureCheckpoint(t, store, 3)
	if len(third.Passes) != 0 || third.PassRounds != 0 || third.CostUSD != 0 {
		t.Errorf("a checkpoint with no critique reports passes: %+v", third)
	}
}

// A retried round is one round of review that cost twice, not two rounds. The
// human counts rounds to know how many times the PRD was criticised.
func TestRoadmapCountsRoundsNotAttempts(t *testing.T) {
	store := newTestStore(t)
	clock, worker := criticPassFixture(t, store)
	runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000005", 2, 1, 0.10, 5*time.Second)
	clock.at = clock.at.Add(time.Minute)
	runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000006", 2, 1, 0.10, 5*time.Second)

	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if len(checkpoint.Passes) != 2 {
		t.Fatalf("passes = %d, want both submissions listed", len(checkpoint.Passes))
	}
	if checkpoint.PassRounds != 1 {
		t.Errorf("rounds = %d, want 1; the same round was run twice", checkpoint.PassRounds)
	}
	if checkpoint.CostUSD < 0.1999 || checkpoint.CostUSD > 0.2001 {
		t.Errorf("cost = %v, want 0.20; both attempts were paid for", checkpoint.CostUSD)
	}
}

// The live pass replaces the orchestrator's marker file. A submitted critique
// that has not come back is what the page draws as turning.
func TestRoadmapReportsASubmittedCritiqueAsLive(t *testing.T) {
	store := newTestStore(t)
	_, _ = criticPassFixture(t, store)
	submitCritiquePass(t, store, "53000000-0000-4000-8000-000000000007", 2, 3)

	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if checkpoint.Live == nil {
		t.Fatal("a queued critique is not reported as live")
	}
	if checkpoint.Live.Mode != "critique" || checkpoint.Live.Round != 3 {
		t.Errorf("live pass = %+v, want critique round 3", checkpoint.Live)
	}
	if checkpoint.Live.Started.IsZero() {
		t.Error("a live pass has no start time")
	}
	project := readRoadmapForTest(t, store).Projects[0]
	if project.Live == nil || project.Live.Round != 3 {
		t.Errorf("project live pass = %+v, want the checkpoint's", project.Live)
	}
}

// A pass is a line on the Planning page that the operator is meant to open, so
// it has to carry the Work it ran as. The six numbers a pass reports are the
// summary; the findings, the attempts and the reason a round failed live on
// the Work row and nowhere else. Both the finished passes and the live one
// carry it, because "watch the one that is turning" and "read the one that
// failed" are the same click.
func TestRoadmapPassesCarryTheWorkTheyRanAs(t *testing.T) {
	store := newTestStore(t)
	clock, worker := criticPassFixture(t, store)
	finished := runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000021", 2, 1, 0.4, 15*time.Second)
	live := submitCritiquePass(t, store, "53000000-0000-4000-8000-000000000022", 2, 2)

	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if len(checkpoint.Passes) != 2 {
		t.Fatalf("passes = %+v, want the finished round and the live one", checkpoint.Passes)
	}
	if checkpoint.Passes[0].WorkID != finished {
		t.Errorf("finished pass work id = %q, want %q",
			checkpoint.Passes[0].WorkID, finished)
	}
	if checkpoint.Live == nil {
		t.Fatal("the second round was submitted and is not reported as live")
	}
	if checkpoint.Live.WorkID != live.WorkIDs[0] {
		t.Errorf("live pass work id = %q, want %q",
			checkpoint.Live.WorkID, live.WorkIDs[0])
	}
	project := readRoadmapForTest(t, store).Projects[0]
	if project.Live == nil || project.Live.WorkID != live.WorkIDs[0] {
		t.Errorf("project live pass = %+v, want the checkpoint's work id", project.Live)
	}
}

// The waiting list carries the cost and the round count with it, so the bell
// can say what a checkpoint has already cost without a second read.
func TestRoadmapWaitingCarriesPassCostAndRounds(t *testing.T) {
	store := newTestStore(t)
	clock, worker := criticPassFixture(t, store)
	runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000008", 4, 1, 0.33, 5*time.Second)

	roadmap := readRoadmapForTest(t, store)
	if len(roadmap.Waiting) != 1 {
		t.Fatalf("waiting = %+v, want the one checkpoint in review", roadmap.Waiting)
	}
	waiting := roadmap.Waiting[0]
	if waiting.Number != 4 || waiting.PassRounds != 1 || waiting.CostUSD != 0.33 {
		t.Fatalf("waiting entry = %+v, want checkpoint 4 with one round at 0.33", waiting)
	}
}

// A project with no critique at all still renders, with the empty array the
// page reads rather than a null.
func TestRoadmapPassesAreEmptyWithoutACritique(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	project := readRoadmapForTest(t, store).Projects[0]
	if project.CostUSD != 0 || project.Live != nil {
		t.Errorf("project cost and live pass = %v %+v, want zero and nil", project.CostUSD, project.Live)
	}
	for _, checkpoint := range project.Checkpoints {
		if checkpoint.Passes == nil {
			t.Errorf("checkpoint %d passes is nil; the page reads an array", checkpoint.Number)
		}
		if len(checkpoint.Passes) != 0 || checkpoint.PassRounds != 0 || checkpoint.CostUSD != 0 {
			t.Errorf("checkpoint %d reports passes it never had: %+v", checkpoint.Number, checkpoint)
		}
		if checkpoint.Live != nil {
			t.Errorf("checkpoint %d has a live pass with no critique Work: %+v",
				checkpoint.Number, checkpoint.Live)
		}
	}
}

// The light-factory skill reads the findings back over HTTP, so this is the
// route and the field it reads: GET /api/v1/work/{id}, then work.result. The
// JSON is compared byte for byte because Factory promises not to parse it, and
// a reformatted copy would be a parse.
func TestCompletedCritiqueResultIsReadableOverHTTP(t *testing.T) {
	store := newTestStore(t)
	clock, worker := criticPassFixture(t, store)
	workID := runCritiquePass(t, store, clock, worker,
		"53000000-0000-4000-8000-000000000009", 2, 1, 0.42, 10*time.Second)

	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)
	response, err := server.Client().Get(server.URL + "/api/v1/work/" + workID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var detail protocol.WorkDetail
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Work.State != protocol.SessionSucceeded {
		t.Fatalf("state = %q, want succeeded", detail.Work.State)
	}
	if detail.Work.Result != criticFindings {
		t.Fatalf("work.result = %q, want the critic's JSON unchanged", detail.Work.Result)
	}
}
