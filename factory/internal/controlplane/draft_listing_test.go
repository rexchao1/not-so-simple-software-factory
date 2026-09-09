package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// runTaskForTest admits one ordinary, never-draft Run so the draft filter has
// something it must exclude.
func runTaskForTest(t *testing.T, store *Store, requestKey string) protocol.RunDetail {
	t.Helper()
	worker := registerTestWorker(t, store, "worker-draft-listing", 4, protocol.RepositoryRegistration{
		Key: "listing", RemoteIdentity: "github.com/example/listing",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Ordinary " + requestKey, Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: requestKey})
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

// TestRunPageFiltersToDraftState is the server side of the cockpit drafts
// view. Filtering in the browser cannot work: the runs page is bounded, so a
// draft older than one page would become invisible and therefore permanently
// unapprovable from the one screen whose whole job is the human gate.
func TestRunPageFiltersToDraftState(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	ordinary := runTaskForTest(t, store, "listing-ordinary")
	admission := admitDraftForTest(t, store)

	drafts, err := store.RunPage(ctx, protocol.RunDraft, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts.Runs) != 1 {
		t.Fatalf("draft page = %d runs, want 1", len(drafts.Runs))
	}
	if drafts.Runs[0].ID != admission.RunID {
		t.Fatalf("draft page run = %q, want %q", drafts.Runs[0].ID, admission.RunID)
	}
	if drafts.Runs[0].State != protocol.RunDraft {
		t.Fatalf("draft page state = %q, want draft", drafts.Runs[0].State)
	}
	// The cockpit builds every row out of this one response, so the fields it
	// reads have to survive the filtered query.
	if len(drafts.Runs[0].Targets) != 1 || drafts.Runs[0].Targets[0].ID != admission.WorkIDs[0] {
		t.Fatalf("draft page targets = %#v, want work %q", drafts.Runs[0].Targets, admission.WorkIDs[0])
	}
	if drafts.Runs[0].Task.Name == "" || drafts.Runs[0].Targets[0].RepositoryIdentity == "" {
		t.Fatalf("draft page row is missing its name or repository: %#v", drafts.Runs[0])
	}

	unfiltered, err := store.RunPage(ctx, "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(unfiltered.Runs) != 2 {
		t.Fatalf("unfiltered page = %d runs, want 2", len(unfiltered.Runs))
	}
	for _, run := range drafts.Runs {
		if run.ID == ordinary.Run.ID {
			t.Fatal("draft page returned a run that has no draft Work")
		}
	}

	summaries, err := store.RunSummaryPage(ctx, protocol.RunDraft, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries.Runs) != 1 || summaries.Runs[0].ID != admission.RunID {
		t.Fatalf("draft summary page = %#v, want only %q", summaries.Runs, admission.RunID)
	}
}

// TestRunPageDraftFilterPages proves the filter composes with the existing
// cursor rather than silently truncating: every draft is reachable one bounded
// page at a time.
func TestRunPageDraftFilterPages(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	runTaskForTest(t, store, "listing-paged-ordinary")
	wanted := map[string]bool{}
	for index := 1; index <= 3; index++ {
		response, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
			fmt.Sprintf("81000000-0000-4000-8000-00000000000%d", index))
		if err != nil {
			t.Fatal(err)
		}
		wanted[response.RunID] = true
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 4 {
			t.Fatal("draft paging did not terminate")
		}
		page, err := store.RunPage(ctx, protocol.RunDraft, 1, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range page.Runs {
			if !wanted[run.ID] {
				t.Fatalf("draft paging returned an unexpected run %q", run.ID)
			}
			seen[run.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != len(wanted) {
		t.Fatalf("draft paging saw %d runs, want %d", len(seen), len(wanted))
	}
}

// TestListRunsRejectsUnknownState keeps an unsupported filter loud. Silently
// ignoring it would answer a narrowing request with every run, which reads as
// a working filter that quietly is not one.
func TestListRunsRejectsUnknownState(t *testing.T) {
	store := newTestStore(t)
	runTaskForTest(t, store, "listing-unknown-state")
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	response, err := server.Client().Get(server.URL + "/api/v1/runs?state=running")
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

// TestListRunsDraftStateHTTP is the exact request the cockpit drafts view
// makes.
func TestListRunsDraftStateHTTP(t *testing.T) {
	store := newTestStore(t)
	runTaskForTest(t, store, "listing-http-ordinary")
	admission := admitDraftForTest(t, store)
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	response, err := server.Client().Get(server.URL + "/api/v1/runs?state=draft&limit=50")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var page protocol.RunPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 || page.Runs[0].ID != admission.RunID {
		t.Fatalf("draft listing = %#v, want only %q", page.Runs, admission.RunID)
	}
	if len(page.Runs[0].Targets) != 1 || page.Runs[0].Targets[0].ID != admission.WorkIDs[0] {
		t.Fatalf("draft listing targets = %#v", page.Runs[0].Targets)
	}
}
