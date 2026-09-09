package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedRoadmapFixture is the file fixture this suite used to write, seeded as
// rows instead. It is the same project, the same route lines and the same task
// bodies, so the expectations below are the ones the file reader had to meet.
//
// Two things could not be carried over unchanged, and both are the new rules
// doing their job rather than a loss of coverage:
//
//   - Checkpoint 2 was in review with task files beside it. Pebbles now only
//     exist under a frozen checkpoint, so it is frozen here and its pebble
//     assertions are otherwise untouched.
//   - Something still has to be waiting on the human. Checkpoint 4 is the
//     review with no answers that checkpoint 2 used to be.
func seedRoadmapFixture(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.UpsertPlanningProject(ctx, "payer", SavePlanningProjectRequest{
		Title:     "a new payer onboards itself",
		Statement: "Give it a new payer portal and it works, unattended. The office names a payer and walks away.",
		Route:     "# Route: a new payer onboards itself\n",
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	seed := func(number int, title, summary, body, status string) {
		t.Helper()
		if _, err := store.UpsertPlanningCheckpoint(ctx, "payer", number, SavePlanningCheckpointRequest{
			Title: title, Summary: summary, Body: body,
		}); err != nil {
			t.Fatalf("seed checkpoint %d: %v", number, err)
		}
		for _, step := range planningPathTo(t, status) {
			if _, err := store.TransitionPlanningCheckpoint(ctx, "payer", number, step); err != nil {
				t.Fatalf("seed checkpoint %d status: %v", number, err)
			}
		}
	}
	seed(1, "One payer brings itself up live", "the daily ledger allows 20 logins",
		"# Checkpoint 1: One payer brings itself up live\n", "built")
	seed(2, "The dashboard finishes a parked live payer", "the office fills a field into Settings",
		"# Checkpoint 2: The dashboard finishes a parked live payer\n", "frozen")
	// Checkpoint 3 is a route line with no PRD written yet, which is the normal
	// state of a fresh route and has to render.
	seed(3, "A payer's profile is data, not code", "a bring-up writes the adapter itself", "", "planned")
	seed(4, "Calling comes last", "the office dials from the same screen",
		"# Checkpoint 4: Calling comes last\n", "review")
}

// seedRoadmapPebbles cuts checkpoint 2 into the two pebbles the fixture's task
// files described, ungrouped unless the test groups them.
func seedRoadmapPebbles(t *testing.T, store *Store, split SavePlanningPebblesRequest) {
	t.Helper()
	if _, err := store.ReplacePlanningPebbles(context.Background(), "payer", 2, split); err != nil {
		t.Fatalf("seed pebbles: %v", err)
	}
}

func roadmapFixturePebbles() []PlanningPebble {
	return []PlanningPebble{
		{
			Slug:  "01-live-driver",
			Title: "Rebuild a resumed live payer's own driver",
			Body:  "## Rebuild a resumed live payer's own driver\n\nbody\n",
		},
		{
			Slug:  "02-publish",
			Title: "Let a live terminal run publish to the dashboard",
			Body:  "## Let a live terminal run publish to the dashboard\n",
		},
	}
}

func readRoadmapForTest(t *testing.T, store *Store) Roadmap {
	t.Helper()
	roadmap, err := readRoadmap(context.Background(), store)
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	return roadmap
}

func roadmapFixtureCheckpoint(t *testing.T, store *Store, number int) RoadmapCheckpoint {
	t.Helper()
	roadmap := readRoadmapForTest(t, store)
	if len(roadmap.Projects) != 1 {
		t.Fatalf("one project, got %d", len(roadmap.Projects))
	}
	for _, checkpoint := range roadmap.Projects[0].Checkpoints {
		if checkpoint.Number == number {
			return checkpoint
		}
	}
	t.Fatalf("checkpoint %d is missing", number)
	return RoadmapCheckpoint{}
}

func TestRoadmapReadsProjectCheckpointsAndPebbles(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: roadmapFixturePebbles()})
	roadmap := readRoadmapForTest(t, store)
	if !roadmap.Configured {
		t.Fatal("an open store is a configured roadmap")
	}
	if len(roadmap.Projects) != 1 {
		t.Fatalf("one seeded project is one project, got %d", len(roadmap.Projects))
	}
	boulder := roadmap.Projects[0]
	if boulder.Project != "payer" {
		t.Errorf("project = %q, want payer", boulder.Project)
	}
	if boulder.Title != "a new payer onboards itself" {
		t.Errorf("title = %q", boulder.Title)
	}
	if boulder.Statement == "" || boulder.Statement[:7] != "Give it" {
		t.Errorf("statement = %q, want the boulder statement", boulder.Statement)
	}
	if len(boulder.Checkpoints) != 4 {
		t.Fatalf("four seeded checkpoints, got %d", len(boulder.Checkpoints))
	}
	first := boulder.Checkpoints[0]
	if first.Number != 1 || first.Title != "One payer brings itself up live" {
		t.Errorf("first checkpoint = %d %q", first.Number, first.Title)
	}
	if first.Summary != "the daily ledger allows 20 logins" {
		t.Errorf("summary = %q", first.Summary)
	}
	second := boulder.Checkpoints[1]
	if len(second.Pebbles) != 2 {
		t.Fatalf("two seeded pebbles are two pebbles, got %d", len(second.Pebbles))
	}
	if second.Pebbles[0].Ordinal != 1 || second.Pebbles[0].Title != "Rebuild a resumed live payer's own driver" {
		t.Errorf("first pebble = %d %q", second.Pebbles[0].Ordinal, second.Pebbles[0].Title)
	}
	if boulder.BuiltCount != 1 {
		t.Errorf("built count = %d, want 1", boulder.BuiltCount)
	}
}

// Checkpoints come back in route order however they were written, because the
// page draws them as a numbered ladder.
func TestRoadmapCheckpointsAreOrderedByNumber(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	for _, number := range []int{3, 1, 2} {
		if _, err := store.UpsertPlanningCheckpoint(ctx, "payer", number,
			SavePlanningCheckpointRequest{Title: "Checkpoint"}); err != nil {
			t.Fatal(err)
		}
	}
	checkpoints := readRoadmapForTest(t, store).Projects[0].Checkpoints
	for index, checkpoint := range checkpoints {
		if checkpoint.Number != index+1 {
			t.Fatalf("checkpoint at %d is number %d", index, checkpoint.Number)
		}
	}
}

// Planned means a PRD exists. The file reader answered it with "is <n>.md on
// disk"; the tables answer it with "is the body empty", which is the same
// question about the same thing.
func TestRoadmapPlannedMeansAPRDWasWritten(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	if second := roadmapFixtureCheckpoint(t, store, 2); !second.Planned {
		t.Error("a checkpoint with a saved body reports planned")
	}
	if third := roadmapFixtureCheckpoint(t, store, 3); third.Planned {
		t.Error("a route line with no body does not report planned")
	}
}

// Status is the checkpoint's own, and nothing else can move it. The route line
// used to carry a status that a plan file could override; there is one status
// now and it lives in one column.
func TestRoadmapStatusIsTheCheckpointsOwn(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	for number, want := range map[int]string{1: "built", 2: "frozen", 3: "planned", 4: "review"} {
		if got := roadmapFixtureCheckpoint(t, store, number).Status; got != want {
			t.Errorf("checkpoint %d status = %q, want %q", number, got, want)
		}
	}
}

// Waiting is the page the human opens. A checkpoint the factory is still
// building is not waiting on anyone and must not appear there.
func TestRoadmapWaitingHoldsOnlyWhatNeedsTheHuman(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	roadmap := readRoadmapForTest(t, store)
	if len(roadmap.Waiting) != 1 {
		t.Fatalf("one checkpoint in review is one waiting item, got %+v", roadmap.Waiting)
	}
	waiting := roadmap.Waiting[0]
	if waiting.Number != 4 || waiting.Status != "review" || waiting.Action != "Review the plan" {
		t.Errorf("waiting = %+v", waiting)
	}
	if waiting.Project != "payer" {
		t.Errorf("waiting names its project, got %q", waiting.Project)
	}
}

// Review is only waiting until the answers arrive. Once they are saved the
// checkpoint is the agent's turn again, and leaving it on the human's list
// would make the list something they learn to ignore.
func TestRoadmapReviewStopsWaitingOnceAnswersAreSaved(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	if _, err := store.SavePlanningAnswers(context.Background(), "payer", 4,
		SavePlanningAnswersRequest{Answers: "Q1: weekly. Q2: no."}); err != nil {
		t.Fatal(err)
	}
	if waiting := readRoadmapForTest(t, store).Waiting; len(waiting) != 0 {
		t.Fatalf("an answered review is not waiting, got %+v", waiting)
	}
}

func TestRoadmapFogIsAlwaysWaiting(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "fog")
	// Even with answers saved: fog means drafting stopped on questions, and
	// the answers are only the reply that has not been applied yet.
	if _, err := store.SavePlanningAnswers(ctx, "payer", 1,
		SavePlanningAnswersRequest{Answers: "some answers"}); err != nil {
		t.Fatal(err)
	}
	waiting := readRoadmapForTest(t, store).Waiting
	if len(waiting) != 1 || waiting[0].Action != "Answer the questions" {
		t.Fatalf("fog waits for the human, got %+v", waiting)
	}
}

// A frozen checkpoint with no pebbles used to be a third waiting reason, back
// when splitting was a separate command a human had to remember to run. The
// light factory cuts pebbles in the same context that froze the PRD, so a
// frozen checkpoint is the agent's turn and not the human's.
func TestRoadmapFrozenIsNotWaitingWithOrWithoutPebbles(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	if waiting := roadmapWaitingFor(t, store, 2); waiting != nil {
		t.Fatalf("frozen with no pebbles is the agent's turn, got %+v", waiting)
	}
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: roadmapFixturePebbles()})
	if waiting := roadmapWaitingFor(t, store, 2); waiting != nil {
		t.Fatalf("frozen with pebbles is building, not waiting, got %+v", waiting)
	}
}

func roadmapWaitingFor(t *testing.T, store *Store, number int) *RoadmapWaiting {
	t.Helper()
	for _, waiting := range readRoadmapForTest(t, store).Waiting {
		if waiting.Number == number {
			found := waiting
			return &found
		}
	}
	return nil
}

// An empty factory is the default. It reports configured true with nothing in
// it, because a store that has never been planned into is not broken, and it
// is no longer possible to have planning that the server cannot reach.
func TestRoadmapWithNothingPlannedIsConfiguredAndEmpty(t *testing.T) {
	roadmap := readRoadmapForTest(t, newTestStore(t))
	if !roadmap.Configured {
		t.Error("an open store reports configured true; there is no root to point at")
	}
	if len(roadmap.Projects) != 0 || len(roadmap.Waiting) != 0 {
		t.Errorf("an unplanned factory is empty, got %+v", roadmap)
	}
	if roadmap.Projects == nil || roadmap.Waiting == nil {
		t.Error("empty is an array, not null; the page maps over both")
	}
}

func TestRoadmapEndpointServesTheStoredRoadmap(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	handler := NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/roadmap", nil)
	request.Host = "127.0.0.1:8080"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var roadmap Roadmap
	if err := json.Unmarshal(recorder.Body.Bytes(), &roadmap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(roadmap.Projects) != 1 || len(roadmap.Waiting) != 1 {
		t.Fatalf("endpoint returns the stored roadmap, got %+v", roadmap)
	}
	if !roadmap.Configured {
		t.Error("the endpoint reports configured with no root set anywhere")
	}
}

func TestRoadmapGroupsPebblesByTheirStones(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	pebbles := roadmapFixturePebbles()
	pebbles[0].StoneID = "b1"
	pebbles[1].StoneID = "b2"
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{
		Stones: []PlanningStone{
			{ID: "b1", Title: "Rebuild the driver", Statement: "The driver comes back."},
			{ID: "b2", Title: "Publish it", Statement: "The dashboard sees it."},
		},
		Pebbles: pebbles,
	})
	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if len(checkpoint.Stones) != 2 {
		t.Fatalf("two stones, got %d", len(checkpoint.Stones))
	}
	if checkpoint.Stones[0].ID != "b1" || checkpoint.Stones[0].Title != "Rebuild the driver" {
		t.Errorf("first stone = %+v", checkpoint.Stones[0])
	}
	if checkpoint.Stones[0].Statement != "The driver comes back." {
		t.Errorf("statement = %q", checkpoint.Stones[0].Statement)
	}
	if len(checkpoint.Stones[1].Pebbles) != 1 || checkpoint.Stones[1].Pebbles[0].Slug != "02-publish" {
		t.Errorf("second stone pebbles = %+v", checkpoint.Stones[1].Pebbles)
	}
	if len(checkpoint.Pebbles) != 2 {
		t.Errorf("the flat pebble list survives grouping, got %d", len(checkpoint.Pebbles))
	}
}

func TestRoadmapWithoutAnyGroupingIsOneStone(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: roadmapFixturePebbles()})
	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if len(checkpoint.Stones) != 1 {
		t.Fatalf("no grouping is one stone, got %d", len(checkpoint.Stones))
	}
	if checkpoint.Stones[0].Title != "Everything in this checkpoint" {
		t.Errorf("title = %q", checkpoint.Stones[0].Title)
	}
	if len(checkpoint.Stones[0].Pebbles) != 2 {
		t.Errorf("it holds every pebble, got %d", len(checkpoint.Stones[0].Pebbles))
	}
}

// A grouping that covers some pebbles and forgets others must not hide the
// ones it forgot. They go into a trailing catch-all.
func TestRoadmapGroupingNeverHidesAPebble(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	pebbles := roadmapFixturePebbles()
	pebbles[0].StoneID = "b1"
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{
		Stones:  []PlanningStone{{ID: "b1", Title: "Rebuild the driver"}},
		Pebbles: pebbles,
	})
	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if len(checkpoint.Stones) != 2 {
		t.Fatalf("the ungrouped pebble gets a catch-all, got %d stones", len(checkpoint.Stones))
	}
	if got := len(checkpoint.Stones[0].Pebbles); got != 1 {
		t.Errorf("the named stone holds its one pebble, got %d", got)
	}
	catchAll := checkpoint.Stones[1]
	if catchAll.ID != "B2" || catchAll.Title != "The rest of the checkpoint" {
		t.Errorf("catch-all = %+v", catchAll)
	}
	if len(catchAll.Pebbles) != 1 || catchAll.Pebbles[0].Slug != "02-publish" {
		t.Errorf("catch-all pebbles = %+v", catchAll.Pebbles)
	}
}

// A stone that ended up holding nothing is dropped rather than drawn as an
// empty box, the same way a manifest stone naming no real pebble was.
func TestRoadmapDropsAStoneWithNoPebbles(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	pebbles := roadmapFixturePebbles()
	pebbles[0].StoneID = "b1"
	pebbles[1].StoneID = "b1"
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{
		Stones: []PlanningStone{
			{ID: "b1", Title: "Everything"},
			{ID: "b2", Title: "An empty stone"},
		},
		Pebbles: pebbles,
	})
	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if len(checkpoint.Stones) != 1 || checkpoint.Stones[0].ID != "b1" {
		t.Fatalf("stones = %+v, want the empty one dropped", checkpoint.Stones)
	}
}

func TestRoadmapPebbleSummaryIsTheOpeningParagraph(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	pebbles := roadmapFixturePebbles()
	pebbles[0].Body = "## Rebuild a resumed live payer's own driver\n\n### What are we building?\nThe runner loses the driver\non a resume.\n\n### Why?\nBecause it does.\n"
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: pebbles})
	checkpoint := roadmapFixtureCheckpoint(t, store, 2)
	if got := checkpoint.Pebbles[0].Summary; got != "The runner loses the driver on a resume." {
		t.Errorf("summary = %q", got)
	}
}

func TestRoadmapRollUpRanksTroubleOverProgressOverDone(t *testing.T) {
	cases := []struct {
		name   string
		states []string
		want   string
	}{
		{"nothing started", []string{"", ""}, "planned"},
		{"one running", []string{"succeeded", "running"}, "working"},
		{"running outranks failed", []string{"failed", "running"}, "working"},
		{"failed outranks done", []string{"succeeded", "failed"}, "failed"},
		{"all terminal", []string{"succeeded", "no-change"}, "done"},
		{"partly done", []string{"succeeded", ""}, "part"},
		{"waiting on an answer is working", []string{"needs-input"}, "working"},
		{"a pull request is still working", []string{"ready"}, "working"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pebbles := make([]RoadmapPebble, 0, len(tc.states))
			for _, state := range tc.states {
				pebbles = append(pebbles, RoadmapPebble{State: state})
			}
			if got := roadmapRollUp(pebbles); got != tc.want {
				t.Errorf("roll up %v = %q, want %q", tc.states, got, tc.want)
			}
		})
	}
	if got := roadmapRollUp(nil); got != "planned" {
		t.Errorf("an empty stone = %q, want planned", got)
	}
}

func TestRoadmapApplyWorkStampsPebblesAndColoursStones(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	pebbles := roadmapFixturePebbles()
	pebbles[0].StoneID = "b1"
	pebbles[1].StoneID = "b2"
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{
		Stones: []PlanningStone{
			{ID: "b1", Title: "Rebuild the driver"},
			{ID: "b2", Title: "Publish it"},
		},
		Pebbles: pebbles,
	})
	roadmap := readRoadmapForTest(t, store)
	roadmapApplyWork(&roadmap, roadmapWorkJoin{
		ByID: map[string]roadmapWorkState{},
		ByName: map[string]roadmapWorkState{
			"Rebuild a resumed live payer's own driver":        {State: "running", WorkID: "w1"},
			"Let a live terminal run publish to the dashboard": {State: "succeeded", WorkID: "w2", PullRequestURL: "https://example.test/pr/2"},
		},
	})
	checkpoint := roadmapCheckpointFrom(t, roadmap, 2)
	if checkpoint.Stones[0].State != "working" {
		t.Errorf("a running pebble colours its stone working, got %q", checkpoint.Stones[0].State)
	}
	if checkpoint.Stones[1].State != "done" {
		t.Errorf("a succeeded pebble colours its stone done, got %q", checkpoint.Stones[1].State)
	}
	if got := checkpoint.Stones[1].Pebbles[0].PullRequestURL; got != "https://example.test/pr/2" {
		t.Errorf("the pebble carries its pull request, got %q", got)
	}
	if checkpoint.Pebbles[0].WorkID != "w1" {
		t.Errorf("the flat list is stamped too, got %q", checkpoint.Pebbles[0].WorkID)
	}
}

// A pebble that recorded the Work it was admitted as is joined by that id, not
// by its title. Two pebbles can share a title; they cannot share an id.
func TestRoadmapApplyWorkPrefersTheRecordedWorkID(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	pebbles := roadmapFixturePebbles()
	pebbles[0].WorkID = "work-exact"
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: pebbles})
	roadmap := readRoadmapForTest(t, store)
	roadmapApplyWork(&roadmap, roadmapWorkJoin{
		ByID: map[string]roadmapWorkState{
			"work-exact": {State: "succeeded", WorkID: "work-exact", PullRequestURL: "https://example.test/pr/9"},
		},
		ByName: map[string]roadmapWorkState{
			"Rebuild a resumed live payer's own driver": {State: "failed", WorkID: "work-wrong"},
		},
	})
	pebble := roadmapCheckpointFrom(t, roadmap, 2).Pebbles[0]
	if pebble.WorkID != "work-exact" || pebble.State != "succeeded" {
		t.Errorf("pebble = %+v, want the id join to win over the title join", pebble)
	}
	if pebble.PullRequestURL != "https://example.test/pr/9" {
		t.Errorf("pull request = %q", pebble.PullRequestURL)
	}
}

// A recorded work id that the recent pages do not reach keeps its id and takes
// no state. The Work is real and merely older than the join reads, so falling
// back to a title match would be a guess dressed as a fact.
func TestRoadmapApplyWorkDoesNotFallBackToTitleForAKnownWorkID(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	pebbles := roadmapFixturePebbles()
	pebbles[0].WorkID = "work-old"
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: pebbles})
	roadmap := readRoadmapForTest(t, store)
	roadmapApplyWork(&roadmap, roadmapWorkJoin{
		ByID: map[string]roadmapWorkState{},
		ByName: map[string]roadmapWorkState{
			"Rebuild a resumed live payer's own driver": {State: "running", WorkID: "somebody-elses"},
		},
	})
	pebble := roadmapCheckpointFrom(t, roadmap, 2).Pebbles[0]
	if pebble.WorkID != "work-old" || pebble.State != "" {
		t.Errorf("pebble = %+v, want its own id kept and no borrowed state", pebble)
	}
}

func TestRoadmapApplyWorkIgnoresUnrelatedNames(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: roadmapFixturePebbles()})
	roadmap := readRoadmapForTest(t, store)
	roadmapApplyWork(&roadmap, roadmapWorkJoin{
		ByID:   map[string]roadmapWorkState{},
		ByName: map[string]roadmapWorkState{"Something else entirely": {State: "running"}},
	})
	for _, checkpoint := range roadmap.Projects[0].Checkpoints {
		for _, pebble := range checkpoint.Pebbles {
			if pebble.State != "" {
				t.Errorf("%s took a state it did not earn: %q", pebble.Slug, pebble.State)
			}
		}
	}
}

// A pebble marked built by hand, with no Work row behind it, reports state
// built and carries its ref, so the page can say it is finished without a
// factory run to point at.
func TestRoadmapPebbleBuiltByHandReportsBuiltWithNoWork(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: roadmapFixturePebbles()})
	ctx := context.Background()
	if _, err := store.MarkPlanningPebbleBuilt(ctx, "payer", 2, "01-live-driver",
		MarkPlanningPebbleBuiltRequest{Ref: "https://example.test/pr/7"}); err != nil {
		t.Fatalf("mark built: %v", err)
	}
	if _, err := store.MarkPlanningPebbleBuilt(ctx, "payer", 2, "02-publish",
		MarkPlanningPebbleBuiltRequest{Ref: "https://example.test/pr/8"}); err != nil {
		t.Fatalf("mark built: %v", err)
	}
	roadmap := readRoadmapForTest(t, store)
	roadmapApplyWork(&roadmap, roadmapWorkJoin{
		ByID:   map[string]roadmapWorkState{},
		ByName: map[string]roadmapWorkState{},
	})
	pebble := roadmapCheckpointFrom(t, roadmap, 2).Pebbles[0]
	if pebble.State != "built" {
		t.Errorf("state = %q, want built", pebble.State)
	}
	if pebble.BuiltRef != "https://example.test/pr/7" {
		t.Errorf("built_ref = %q", pebble.BuiltRef)
	}
	if got := roadmapCheckpointFrom(t, roadmap, 2).Stones[0].State; got != "done" {
		t.Errorf("stone state = %q, want done, since built is a terminal success", got)
	}
}

// A pebble that has both a hand entry and a matching Work row keeps the
// Work's state: a real run is stronger evidence than a hand entry.
func TestRoadmapPebbleWithAMatchingWorkRowKeepsTheWorksState(t *testing.T) {
	store := newTestStore(t)
	seedRoadmapFixture(t, store)
	seedRoadmapPebbles(t, store, SavePlanningPebblesRequest{Pebbles: roadmapFixturePebbles()})
	if _, err := store.MarkPlanningPebbleBuilt(context.Background(), "payer", 2, "01-live-driver",
		MarkPlanningPebbleBuiltRequest{Ref: "hand-entered"}); err != nil {
		t.Fatalf("mark built: %v", err)
	}
	roadmap := readRoadmapForTest(t, store)
	roadmapApplyWork(&roadmap, roadmapWorkJoin{
		ByID: map[string]roadmapWorkState{},
		ByName: map[string]roadmapWorkState{
			"Rebuild a resumed live payer's own driver": {State: "running", WorkID: "w1"},
		},
	})
	pebble := roadmapCheckpointFrom(t, roadmap, 2).Pebbles[0]
	if pebble.State != "running" || pebble.WorkID != "w1" {
		t.Errorf("pebble = %+v, want the Work's state to win", pebble)
	}
}

func roadmapCheckpointFrom(t *testing.T, roadmap Roadmap, number int) RoadmapCheckpoint {
	t.Helper()
	for _, checkpoint := range roadmap.Projects[0].Checkpoints {
		if checkpoint.Number == number {
			return checkpoint
		}
	}
	t.Fatalf("checkpoint %d is missing", number)
	return RoadmapCheckpoint{}
}
