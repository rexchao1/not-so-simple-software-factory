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
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

// storedSubmittedName reads the column out of the row itself, so these tests
// prove what was written rather than what some loader chose to surface.
func storedSubmittedName(t *testing.T, store *Store, taskID string) string {
	t.Helper()
	var value string
	err := store.db.QueryRowContext(context.Background(),
		`SELECT submitted_name FROM tasks WHERE id = ?`, taskID).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestSubmittedNameDefaultsToEmptyForRowsWrittenWithoutIt is the migration
// test, written against behaviour rather than DDL text: every Task that
// already existed when migration 036 ran was written without the column, and
// an empty submitted name is the correct answer for all of them. The NULL
// write proves the NOT NULL half, so no loader has to defend against a null.
func TestSubmittedNameDefaultsToEmptyForRowsWrittenWithoutIt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	// The column list deliberately omits submitted_name, the way every
	// pre-036 INSERT did.
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO tasks(
			id, name, name_key, prompt, runtime, timeout_seconds,
			generation, created_at, updated_at
		) VALUES ('task-pre-036', 'Legacy Task', 'legacy task', 'Review it.', 'codex', 3600, 1, 1, 1)
	`)
	if err != nil {
		t.Fatal(err)
	}
	if value := storedSubmittedName(t, store, "task-pre-036"); value != "" {
		t.Fatalf("pre-migration Task submitted_name = %q, want the empty string", value)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE tasks SET submitted_name = NULL WHERE id = 'task-pre-036'`); err == nil {
		t.Fatal("submitted_name accepted NULL; the column is not NOT NULL")
	}
}

// TestOrdinaryTaskCreationLeavesSubmittedNameEmpty pins the normal case.
// Only admission has a name distinct from the one it stores, so a Task made
// through the ordinary path must carry no submitted name at all rather than a
// duplicate of its own.
func TestOrdinaryTaskCreationLeavesSubmittedNameEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	worker := registerTestWorker(t, store, "worker-submitted-name", 4, protocol.RepositoryRegistration{
		Key: "submitted", RemoteIdentity: "github.com/example/submitted",
	})
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Review the dependency graph", Prompt: "Review the repository.",
		Runtime: protocol.RuntimeCodex, RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if value := storedSubmittedName(t, store, task.ID); value != "" {
		t.Fatalf("ordinary Task submitted_name = %q, want the empty string", value)
	}
	detail, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{
		RequestKey: "submitted-name-ordinary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Task.SubmittedName != "" {
		t.Fatalf("ordinary Run task.submitted_name = %q, want the empty string",
			detail.Run.Task.SubmittedName)
	}
	if detail.Run.Task.Name != "Review the dependency graph" {
		t.Fatalf("ordinary Run task.name = %q", detail.Run.Task.Name)
	}
}

// TestAdmitWorkCarriesTheSubmittedNameToTheDraftsAPI is the round trip the
// approval screen depends on. tasks.name has to keep its deduplication suffix
// because tasks.name_key is unique, so the name a human submitted can only
// reach the Drafts view if it is carried separately, all the way through the
// frozen Run snapshot to the exact request the cockpit makes.
func TestAdmitWorkCarriesTheSubmittedNameToTheDraftsAPI(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	const requestKey = "91000000-0000-4000-8000-000000000001"
	const submitted = "Fix flaky test"

	admission, _, err := store.AdmitWork(ctx, protocol.AdmitWorkRequest{
		RequestKey: requestKey,
		Repository: repository.RemoteIdentity,
		Name:       submitted,
		Spec:       "Done when the test stops flaking.",
		Runtime:    admissionRuntime,
		Source:     protocol.WorkSourceCockpit,
	})
	if err != nil {
		t.Fatal(err)
	}

	uniquified := admissionTaskName(requestKey, submitted)
	if uniquified == submitted {
		t.Fatal("admissionTaskName returned the submitted name unchanged; the test proves nothing")
	}
	var storedName string
	if err := store.db.QueryRowContext(ctx,
		`SELECT name FROM tasks WHERE id = ?`, admission.TaskID).Scan(&storedName); err != nil {
		t.Fatal(err)
	}
	if storedName != uniquified {
		t.Fatalf("tasks.name = %q, want %q", storedName, uniquified)
	}
	if value := storedSubmittedName(t, store, admission.TaskID); value != submitted {
		t.Fatalf("tasks.submitted_name = %q, want %q", value, submitted)
	}

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
	// Both halves matter. The suffixed name has to survive, because it is the
	// Task's real identity; the clean one has to arrive, because it is what
	// the approval gate shows a human.
	if page.Runs[0].Task.Name != uniquified {
		t.Fatalf("draft listing task.name = %q, want %q", page.Runs[0].Task.Name, uniquified)
	}
	if page.Runs[0].Task.SubmittedName != submitted {
		t.Fatalf("draft listing task.submitted_name = %q, want %q",
			page.Runs[0].Task.SubmittedName, submitted)
	}
	if !strings.Contains(page.Runs[0].Task.Name, page.Runs[0].Task.SubmittedName) {
		t.Fatalf("the two names disagree: %q and %q",
			page.Runs[0].Task.Name, page.Runs[0].Task.SubmittedName)
	}
}

// TestAdmitWorkBoundsAnOverlongSubmittedName covers the one case where the
// submitted name is not simply stored. admissionTaskName already truncates a
// long title by rune on its way into tasks.name, and the submitted copy is
// bounded the same way rather than rejected: refusing it would turn a
// submission that admits today into a hard failure over a field that changes
// nothing about what runs. The title is multibyte so the rune bound is also
// proved against the column's byte-length CHECK.
func TestAdmitWorkBoundsAnOverlongSubmittedName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	submitted := strings.Repeat("漢", 300)

	admission, _, err := store.AdmitWork(ctx, protocol.AdmitWorkRequest{
		RequestKey: "91000000-0000-4000-8000-000000000002",
		Repository: repository.RemoteIdentity,
		Name:       submitted,
		Spec:       "Done when the long title is bounded.",
		Runtime:    admissionRuntime,
		Source:     protocol.WorkSourceCockpit,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := storedSubmittedName(t, store, admission.TaskID)
	if runes := utf8.RuneCountInString(stored); runes != maxTaskNameRunes {
		t.Fatalf("stored submitted name = %d runes, want %d", runes, maxTaskNameRunes)
	}
	if !strings.HasPrefix(submitted, stored) {
		t.Fatalf("stored submitted name is not a prefix of the submitted one: %q", stored)
	}
}
