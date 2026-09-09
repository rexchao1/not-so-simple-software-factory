package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// multiRepositoryRun admits one Task across several repositories, which is the
// case the Work board exists to fix: one Run, several independent Work items.
func multiRepositoryRun(
	t *testing.T, store *Store, requestKey string, identities ...string,
) (protocol.RunDetail, []protocol.ManagedRepository) {
	t.Helper()
	repositories := make([]protocol.ManagedRepository, 0, len(identities))
	repositoryIDs := make([]string, 0, len(identities))
	for _, identity := range identities {
		repository := registerTestRepository(t, store, identity)
		repositories = append(repositories, repository)
		repositoryIDs = append(repositoryIDs, repository.ID)
	}
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Fix worker queue lease guard " + requestKey, Prompt: "Guard the claim lease.",
		Runtime: protocol.RuntimeClaudeCode, TimeoutSeconds: 3600, ConcurrencyLimit: 3,
		RepositoryIDs: repositoryIDs, Schedule: protocol.TaskSchedule{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID,
		protocol.RunTaskRequest{RequestKey: requestKey})
	if err != nil {
		t.Fatal(err)
	}
	return run, repositories
}

func workIDs(page protocol.WorkListPage) []string {
	ids := make([]string, 0, len(page.Work))
	for _, item := range page.Work {
		ids = append(ids, item.ID)
	}
	return ids
}

// TestWorkPageSplitsAMultiRepositoryRun is the headline acceptance criterion:
// a Run with three repository targets produces three independently listed Work
// items, and each repository tab shows only its own.
func TestWorkPageSplitsAMultiRepositoryRun(t *testing.T) {
	store := newTestStore(t)
	run, repositories := multiRepositoryRun(t, store, "split-1",
		"github.com/example/factory", "github.com/example/orchestrator", "github.com/example/site")

	all, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Work) != 3 {
		t.Fatalf("unfiltered Work page has %d items, want 3", len(all.Work))
	}
	for _, item := range all.Work {
		if item.RunID != run.Run.ID {
			t.Fatalf("Work %s belongs to run %s, want %s", item.ID, item.RunID, run.Run.ID)
		}
		if item.RepositoryIdentity == "" || item.TaskName == "" {
			t.Fatalf("Work card is missing its repository or title: %+v", item)
		}
	}

	for _, repository := range repositories {
		page, err := store.WorkPage(context.Background(),
			protocol.WorkFilter{RepositoryID: repository.ID}, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Work) != 1 {
			t.Fatalf("repository %s tab has %d items, want 1", repository.RemoteIdentity, len(page.Work))
		}
		if page.Work[0].RepositoryIdentity != repository.RemoteIdentity {
			t.Fatalf("repository tab %s showed Work for %s",
				repository.RemoteIdentity, page.Work[0].RepositoryIdentity)
		}
	}
}

func TestWorkPageFiltersByStateAndRun(t *testing.T) {
	store := newTestStore(t)
	run, _ := multiRepositoryRun(t, store, "filter-1",
		"github.com/example/one", "github.com/example/two")
	other, _ := multiRepositoryRun(t, store, "filter-2", "github.com/example/three")

	byRun, err := store.WorkPage(context.Background(),
		protocol.WorkFilter{RunID: run.Run.ID}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(byRun.Work) != 2 {
		t.Fatalf("run filter returned %d items, want 2", len(byRun.Work))
	}

	byOther, err := store.WorkPage(context.Background(),
		protocol.WorkFilter{RunID: other.Run.ID}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(byOther.Work) != 1 {
		t.Fatalf("sibling run filter returned %d items, want 1", len(byOther.Work))
	}

	// Every admitted item is blocked here: no worker is registered, so none of
	// them can route. Asking for a state nothing is in must return nothing
	// rather than everything.
	succeeded, err := store.WorkPage(context.Background(),
		protocol.WorkFilter{States: []protocol.SessionState{protocol.SessionSucceeded}}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(succeeded.Work) != 0 {
		t.Fatalf("succeeded filter returned %d items, want 0", len(succeeded.Work))
	}

	blocked, err := store.WorkPage(context.Background(), protocol.WorkFilter{
		States: []protocol.SessionState{protocol.SessionBlocked, protocol.SessionQueued},
	}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Work) != 3 {
		t.Fatalf("blocked or queued filter returned %d items, want 3", len(blocked.Work))
	}
}

func TestWorkPageRejectsAnUnknownState(t *testing.T) {
	store := newTestStore(t)
	_, err := store.WorkPage(context.Background(),
		protocol.WorkFilter{States: []protocol.SessionState{"'; DROP TABLE sessions; --"}}, 50, "")
	requireServiceError(t, err, "invalid_state")
}

// TestWorkPagePlanningFilter is the store-level acceptance criterion: exclude
// hides a session with a planning triple, only returns just that session, all
// returns both, and the zero value of the filter struct behaves like all so
// in-process callers such as roadmapWork see no change.
func TestWorkPagePlanningFilter(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "planning-build", "github.com/example/build")

	planningResponse, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: "planning-1", Planning: planningTripleForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}

	all, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Work) != 2 {
		t.Fatalf("empty filter returned %d items, want 2 (build + planning)", len(all.Work))
	}
	for _, item := range all.Work {
		if item.RunID == planningResponse.RunID && !item.Planning {
			t.Fatalf("planning Work %s did not carry planning: true", item.ID)
		}
		if item.RunID != planningResponse.RunID && item.Planning {
			t.Fatalf("build Work %s wrongly carried planning: true", item.ID)
		}
	}

	excluded, err := store.WorkPage(context.Background(),
		protocol.WorkFilter{Planning: protocol.PlanningFilterExclude}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded.Work) != 1 || excluded.Work[0].RunID == planningResponse.RunID {
		t.Fatalf("exclude filter returned %+v, want just the build Work", excluded.Work)
	}

	only, err := store.WorkPage(context.Background(),
		protocol.WorkFilter{Planning: protocol.PlanningFilterOnly}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(only.Work) != 1 || only.Work[0].RunID != planningResponse.RunID || !only.Work[0].Planning {
		t.Fatalf("only filter returned %+v, want the single planning Work", only.Work)
	}

	both, err := store.WorkPage(context.Background(),
		protocol.WorkFilter{Planning: protocol.PlanningFilterAll}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(both.Work) != 2 {
		t.Fatalf("all filter returned %d items, want 2", len(both.Work))
	}
}

func TestWorkPageRejectsAnUnknownPlanningFilter(t *testing.T) {
	store := newTestStore(t)
	_, err := store.WorkPage(context.Background(),
		protocol.WorkFilter{Planning: protocol.PlanningFilter("sideways")}, 50, "")
	requireServiceError(t, err, "invalid_planning_filter")
}

// TestWorkPagePagesWithoutRepeatingOrSkipping walks every page of a listing
// and checks the union, because a cursor that repeats a row or drops one is
// the classic pagination fault and is invisible on a single page.
func TestWorkPagePagesWithoutRepeatingOrSkipping(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "page-1",
		"github.com/example/a", "github.com/example/b", "github.com/example/c")
	multiRepositoryRun(t, store, "page-2", "github.com/example/d", "github.com/example/e")

	seen := map[string]int{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range workIDs(page) {
			seen[id]++
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("paging saw %d distinct Work items, want 5", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("Work %s appeared %d times across pages", id, count)
		}
	}
}

func TestWorkPageRejectsAnInvalidLimit(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 500, ""); err == nil {
		t.Fatal("an oversized limit was accepted")
	}
	if _, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, -1, ""); err == nil {
		t.Fatal("a negative limit was accepted")
	}
}

// TestWorkPageLeavesUnreportedCostUnset is the honesty rule: a runtime that
// reports no cost must produce no figure, never a zero that reads as "this was
// free".
func TestWorkPageLeavesUnreportedCostUnset(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "cost-1", "github.com/example/costless")
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Work) != 1 {
		t.Fatalf("page has %d items, want 1", len(page.Work))
	}
	if page.Work[0].CostUSD != nil {
		t.Fatalf("unreported cost surfaced as %v", *page.Work[0].CostUSD)
	}
}

// TestWorkPageCarriesTheOrchestratorBrief checks the card's brief preview
// reaches the list without a Run detail fetch.
func TestWorkPageCarriesTheOrchestratorBrief(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, admissionRepositoryIdentity)
	if _, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: "brief-00000000-0000-4000-8000-000000000001",
		Repository: admissionRepositoryIdentity, Name: "Briefed", Spec: "Do the work.",
		Runtime: string(admissionRuntime), Source: protocol.WorkSourceOrchestrator,
		PreApproved: true,
		Brief: &protocol.WorkBrief{
			Context: "Worker claim routing", Why: "Queued work is stalled",
			Risk: "High", Work: "Go scheduler checks",
		},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Work) != 1 || page.Work[0].Brief == nil {
		t.Fatalf("brief did not reach the Work list: %+v", page.Work)
	}
	if page.Work[0].Brief.Why != "Queued work is stalled" {
		t.Fatalf("brief = %+v", page.Work[0].Brief)
	}
}

// TestWorkPageOmitsAnAbsentBrief keeps the card honest for the ordinary case:
// most Work has no orchestrator brief and must not grow an empty one.
func TestWorkPageOmitsAnAbsentBrief(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "nobrief-1", "github.com/example/plain")
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Work) != 1 {
		t.Fatalf("page has %d items, want 1", len(page.Work))
	}
	if page.Work[0].Brief != nil {
		t.Fatalf("Work with no brief reported %+v", page.Work[0].Brief)
	}
}

// TestWorkPageReportsStageProgress covers the card's "2 / 4 · Review" line.
func TestWorkPageReportsStageProgress(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "stages-1", "github.com/example/staged")
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	item := page.Work[0]
	if item.StageCount < 1 {
		t.Fatalf("Work reported %d stages, want at least one", item.StageCount)
	}
	if item.CompletedStages != 0 {
		t.Fatalf("unstarted Work reported %d completed stages", item.CompletedStages)
	}
	if item.CurrentStage == nil {
		t.Fatal("Work reported no current stage")
	}
	if item.CurrentStage.Name == "" {
		t.Fatalf("current stage has no name: %+v", item.CurrentStage)
	}
}

// TestWorkPageMarksBlockedWorkForAttention mirrors the Run-level rule so a
// card and its parent Run cannot disagree about what needs a person.
func TestWorkPageMarksBlockedWorkForAttention(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "attention-1", "github.com/example/unroutable")
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	item := page.Work[0]
	if item.State != protocol.SessionBlocked {
		t.Fatalf("state = %q, want blocked with no eligible worker", item.State)
	}
	if !item.NeedsAttention {
		t.Fatalf("Work blocked for %q was not marked for attention", item.BlockedReason)
	}
}

// TestSessionUpdatedAtTracksWrites covers the migration 045 trigger. Twenty-
// five statements across nine files update a session and none of them names
// this column, so if the trigger stops firing every card's relative time
// silently freezes at admission.
func TestSessionUpdatedAtTracksWrites(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, admissionRepositoryIdentity)
	response, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: "touch-00000000-0000-4000-8000-000000000001",
		Repository: admissionRepositoryIdentity, Name: "Touched", Spec: "Do the work.",
		Runtime: string(admissionRuntime), Source: protocol.WorkSourceOrchestrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := response.WorkIDs[0]

	var admitted, before int64
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT admitted_at, updated_at FROM sessions WHERE id = ?`, workID,
	).Scan(&admitted, &before); err != nil {
		t.Fatal(err)
	}
	if before < admitted {
		t.Fatalf("updated_at %d predates admitted_at %d", before, admitted)
	}

	// Park the column on a value no clock will produce, so the assertion below
	// is about whether the trigger fired and not about how many milliseconds
	// the two writes happened to be apart.
	//
	// Comparing the admission's own timestamp against the approval's does not
	// work: the trigger stores wall-clock milliseconds, admission and approval
	// routinely land inside the same millisecond on a warm database, and the
	// test then failed roughly one run in four for a column that was working
	// exactly as designed. Writing the column explicitly is also the trigger's
	// documented escape hatch, so this parking write is left alone by it and
	// only the approval that follows can move the value.
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE sessions SET updated_at = 1 WHERE id = ?`, workID); err != nil {
		t.Fatal(err)
	}

	// Approval is an ordinary update that names no timestamp of its own, which
	// is exactly the case the trigger exists for.
	if _, err := store.ApproveWork(context.Background(), workID,
		protocol.ApproveWorkRequest{Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT updated_at FROM sessions WHERE id = ?`, workID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= 1 {
		t.Fatalf("updated_at did not advance on approval: parked at 1, then %d", after)
	}
	if after < admitted {
		t.Fatalf("updated_at %d predates the admission it belongs to, %d", after, admitted)
	}

	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Work[0].UpdatedAt.UnixMilli(); got != after {
		t.Fatalf("Work list reported updated_at %d, want the stored %d", got, after)
	}
}

// TestListWorkHTTP is the exact request the Work board makes, including the
// repeated state parameter and the repository filter a tab applies.
func TestListWorkHTTP(t *testing.T) {
	store := newTestStore(t)
	_, repositories := multiRepositoryRun(t, store, "http-1",
		"github.com/example/alpha", "github.com/example/beta")
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	get := func(t *testing.T, path string) protocol.WorkListPage {
		t.Helper()
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, response.StatusCode)
		}
		var page protocol.WorkListPage
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	all := get(t, "/api/v1/work?limit=50")
	if len(all.Work) != 2 {
		t.Fatalf("unfiltered board returned %d items, want 2", len(all.Work))
	}

	tab := get(t, "/api/v1/work?limit=50&repository_id="+repositories[0].ID)
	if len(tab.Work) != 1 || tab.Work[0].RepositoryIdentity != repositories[0].RemoteIdentity {
		t.Fatalf("repository tab returned %+v", tab.Work)
	}

	// Repeated state parameters are how the board asks for a board column.
	states := get(t, "/api/v1/work?limit=50&state=blocked&state=queued")
	if len(states.Work) != 2 {
		t.Fatalf("state filter returned %d items, want 2", len(states.Work))
	}
	empty := get(t, "/api/v1/work?limit=50&state=succeeded")
	if len(empty.Work) != 0 {
		t.Fatalf("succeeded filter returned %d items, want 0", len(empty.Work))
	}
}

func TestListWorkHTTPRejectsAnUnknownState(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "http-2", "github.com/example/gamma")
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	response, err := server.Client().Get(server.URL + "/api/v1/work?state=in-progress")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	var errorBody protocol.ErrorBody
	if err := json.NewDecoder(response.Body).Decode(&errorBody); err != nil {
		t.Fatal(err)
	}
	if errorBody.Error.Code != "invalid_state" {
		t.Fatalf("error code = %q, want invalid_state", errorBody.Error.Code)
	}
}

// TestListWorkHTTPDefaultsToExcludingPlanning is the HTTP-layer half of the
// planning filter: a bare GET must hide planning passes even though the
// store's own zero value means everything, and each planning=... value must
// behave as documented.
func TestListWorkHTTPDefaultsToExcludingPlanning(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "planning-http-build", "github.com/example/http-build")
	planningResponse, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: "planning-http-1", Planning: planningTripleForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	get := func(t *testing.T, path string) protocol.WorkListPage {
		t.Helper()
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, response.StatusCode)
		}
		var page protocol.WorkListPage
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	byDefault := get(t, "/api/v1/work?limit=50")
	for _, item := range byDefault.Work {
		if item.RunID == planningResponse.RunID {
			t.Fatalf("default board request returned planning Work %s", item.ID)
		}
	}
	if len(byDefault.Work) != 1 {
		t.Fatalf("default board request returned %d items, want 1", len(byDefault.Work))
	}

	only := get(t, "/api/v1/work?limit=50&planning=only")
	if len(only.Work) != 1 || only.Work[0].RunID != planningResponse.RunID || !only.Work[0].Planning {
		t.Fatalf("planning=only returned %+v, want the planning Work", only.Work)
	}

	all := get(t, "/api/v1/work?limit=50&planning=all")
	if len(all.Work) != 2 {
		t.Fatalf("planning=all returned %d items, want 2", len(all.Work))
	}

	response, err := server.Client().Get(server.URL + "/api/v1/work?planning=sideways")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("planning=sideways status = %d, want 400", response.StatusCode)
	}
	var errorBody protocol.ErrorBody
	if err := json.NewDecoder(response.Body).Decode(&errorBody); err != nil {
		t.Fatal(err)
	}
	if errorBody.Error.Code != "invalid_planning_filter" {
		t.Fatalf("error code = %q, want invalid_planning_filter", errorBody.Error.Code)
	}
}

// TestAdmitWorkStillPostsToTheSamePath guards the route pair: GET and POST
// on /api/v1/work are different operations and Go's mux separates them by
// method. A regression here would break every admission client.
func TestAdmitWorkStillPostsToTheSamePath(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, admissionRepositoryIdentity)
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	body := `{"request_key":"route-00000000-0000-4000-8000-000000000001",` +
		`"repository":"` + admissionRepositoryIdentity + `","name":"Routed",` +
		`"spec":"Do the work.","runtime":"` + string(admissionRuntime) + `","source":"orchestrator"}`
	response, err := server.Client().Post(server.URL+"/api/v1/work", "application/json",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/work status = %d, want 201", response.StatusCode)
	}
}

// TestWorkPageCountsCodeStageChecks covers the card's verification line. The
// list counts only code stages, whose exit status Factory owns, and needs no
// stage result text to do it.
func TestWorkPageCountsCodeStageChecks(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "checks-1", "github.com/example/checked")
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	item := page.Work[0]
	// The default pipeline has no code stage, so the card reports no checks
	// rather than an empty summary that reads as "nothing failed".
	if item.Verification != nil {
		t.Fatalf("a pipeline with no code stage reported %+v", item.Verification)
	}

	// Give the Work a code stage and confirm the counts follow its state.
	if _, err := store.db.ExecContext(context.Background(), `
		INSERT INTO session_stages(session_id, position, name, kind, prompt, command, state)
		VALUES (?, 19, 'Test', 'code', '', 'go test ./...', 'failed')
	`, item.ID); err != nil {
		t.Fatal(err)
	}
	page, err = store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	verification := page.Work[0].Verification
	if verification == nil {
		t.Fatal("a code stage produced no verification summary")
	}
	if verification.RecordedChecks != 1 || verification.Failed != 1 || verification.Passed != 0 {
		t.Fatalf("verification = %+v, want one failed check", verification)
	}
}

// setStages replaces a Work item's stages so the card's "current stage" can be
// exercised for each lifecycle shape.
func setStages(t *testing.T, store *Store, workID string, states ...string) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(),
		`DELETE FROM session_stages WHERE session_id = ?`, workID); err != nil {
		t.Fatal(err)
	}
	for position, state := range states {
		if _, err := store.db.ExecContext(context.Background(), `
			INSERT INTO session_stages(session_id, position, name, kind, prompt, command, state)
			VALUES (?, ?, ?, 'agent', 'do it', '', ?)
		`, workID, position, "Stage"+string(rune('A'+position)), state); err != nil {
			t.Fatal(err)
		}
	}
}

func currentStageName(t *testing.T, store *Store) string {
	t.Helper()
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Work) != 1 {
		t.Fatalf("page has %d items, want 1", len(page.Work))
	}
	if page.Work[0].CurrentStage == nil {
		return ""
	}
	return page.Work[0].CurrentStage.Name
}

// TestWorkPageNamesTheStageWorthShowing covers what the card's stage label
// means in each lifecycle shape. Naming the last stage of Work that has not
// started tells an operator it is about to deliver when it has not begun.
func TestWorkPageNamesTheStageWorthShowing(t *testing.T) {
	store := newTestStore(t)
	multiRepositoryRun(t, store, "current-1", "github.com/example/current")
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	workID := page.Work[0].ID

	for _, testCase := range []struct {
		name   string
		states []string
		want   string
	}{
		// Nothing has run, so the card names the stage that will run first.
		{name: "not started", states: []string{"pending", "pending", "pending"}, want: "StageA"},
		// The running stage is what is happening now.
		{name: "running", states: []string{"succeeded", "running", "pending"}, want: "StageB"},
		// A failure is why the Work stopped, even when later stages were
		// cancelled after it.
		{name: "failed then cancelled", states: []string{"succeeded", "failed", "cancelled"}, want: "StageB"},
		// All done: the furthest stage reached.
		{name: "all succeeded", states: []string{"succeeded", "succeeded", "succeeded"}, want: "StageC"},
		// Interrupted before anything failed.
		{name: "cancelled early", states: []string{"succeeded", "cancelled", "pending"}, want: "StageB"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setStages(t, store, workID, testCase.states...)
			if got := currentStageName(t, store); got != testCase.want {
				t.Fatalf("current stage = %q, want %q for %v", got, testCase.want, testCase.states)
			}
		})
	}
}

// TestWorkPageCostSurvivesAStageReset is the list-side mirror of the detail
// test: a retry wipes stage cost, so the card must read attempts instead or it
// reports a retried Work as cheaper than it was.
func TestWorkPageCostSurvivesAStageReset(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, admissionRepositoryIdentity)
	cost := 0.37
	workID := terminalWorkWithCost(t, store, "listcost-00000000-0000-4000-8000-000000000001", &cost)

	// Wipe every stage's cost exactly as RetrySession does.
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE session_stages SET cost_usd = NULL WHERE session_id = ?`, workID); err != nil {
		t.Fatal(err)
	}
	page, err := store.WorkPage(context.Background(), protocol.WorkFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Work) != 1 || page.Work[0].CostUSD == nil {
		t.Fatalf("cost was lost when the stage rows were reset: %+v", page.Work)
	}
	if *page.Work[0].CostUSD < 0.369 || *page.Work[0].CostUSD > 0.371 {
		t.Fatalf("cost = %v, want the attempt's reported spend", *page.Work[0].CostUSD)
	}
}
