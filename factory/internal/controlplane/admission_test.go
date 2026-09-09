package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

func admitForTest(
	t *testing.T, store *Store, source protocol.WorkSource, preApproved bool, requestKey string,
) (protocol.AdmitWorkResponse, error) {
	t.Helper()
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	return firstTwo(store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey:  requestKey,
		Repository:  repository.RemoteIdentity,
		Name:        "Add a farewell function",
		Spec:        "## Add a farewell function\n\nDone when farewell('world') returns Goodbye, world!",
		Runtime:     admissionRuntime,
		Source:      source,
		PreApproved: preApproved,
	}))
}

// admitDraftForTest creates one draft through the real admission path.
func admitDraftForTest(t *testing.T, store *Store) protocol.AdmitWorkResponse {
	t.Helper()
	response, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"11000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestFastAssuranceIsExplicitAndOrchestratorOnly(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	request := protocol.AdmitWorkRequest{
		RequestKey: "fast-assurance", Repository: repository.RemoteIdentity,
		Name: "Small fix", Spec: "Fix the typo.", Runtime: admissionRuntime,
		Source: protocol.WorkSourceOrchestrator, PreApproved: true,
		Assurance: protocol.AssuranceFast,
	}
	response, _, err := store.AdmitWork(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.Run(t.Context(), response.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Assurance != protocol.AssuranceFast {
		t.Fatalf("assurance = %q, want fast", detail.Run.Assurance)
	}

	request.RequestKey = "fast-from-cockpit"
	request.Source = protocol.WorkSourceCockpit
	request.PreApproved = false
	_, _, err = store.AdmitWork(t.Context(), request)
	requireServiceError(t, err, "fast_assurance_not_permitted")
}

// AC-1. The two assertions on assigned_worker_id and executions distinguish
// "inserted as draft" from "inserted queued, then demoted": a demote-after
// the fact would still leave the session state as draft, but only the
// insert-as-draft path leaves assigned_worker_id NULL and creates no
// executions row, since worker selection never ran. This is what turns the
// no-window guarantee from Step 4b from reviewable into tested.
func TestAdmitWithoutPreApprovalLandsInDraft(t *testing.T) {
	store := newTestStore(t)
	response, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"11000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if response.State != protocol.SessionDraft {
		t.Fatalf("state = %q, want draft", response.State)
	}
	if len(response.WorkIDs) != 1 {
		t.Fatalf("work ids = %d, want 1", len(response.WorkIDs))
	}
	var assignedWorkerID sql.NullString
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT assigned_worker_id FROM sessions WHERE id = ?`, response.WorkIDs[0],
	).Scan(&assignedWorkerID); err != nil {
		t.Fatal(err)
	}
	if assignedWorkerID.Valid {
		t.Fatalf("assigned_worker_id = %q, want NULL; a draft never went through worker selection", assignedWorkerID.String)
	}
	var executions int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM executions WHERE session_id = ?`, response.WorkIDs[0],
	).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if executions != 0 {
		t.Fatalf("executions = %d, want 0; a draft session was queued and demoted rather than inserted as draft", executions)
	}
}

// AC-2: an orchestrator submission starts without further action. "Not
// draft" is too weak to prove that, because a submission that lands blocked
// is also not draft and still needs a Worker to appear before anything
// happens. With a Worker that can take it, the submission must be queued with
// an execution assigned, which is the positive mirror of the executions = 0
// assertion in TestAdmitWithoutPreApprovalLandsInDraft.
func TestOrchestratorPreApprovedSubmissionIsQueued(t *testing.T) {
	store := newTestStore(t)
	worker := eligibleWorkerForAdmission(t, store, "worker-admit")
	response, err := admitForTest(t, store, protocol.WorkSourceOrchestrator, true,
		"11000000-0000-4000-8000-000000000003")
	if err != nil {
		t.Fatal(err)
	}
	if response.State != protocol.SessionQueued {
		t.Fatalf("state = %q, want queued", response.State)
	}
	assertQueued(t, store, response.WorkIDs[0])
	var assignedWorkerID string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT assigned_worker_id FROM sessions WHERE id = ?`, response.WorkIDs[0],
	).Scan(&assignedWorkerID); err != nil {
		t.Fatal(err)
	}
	if assignedWorkerID != worker.ID {
		t.Fatalf("assigned_worker_id = %q, want %q", assignedWorkerID, worker.ID)
	}
	var approvedBy string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT approved_by FROM sessions WHERE id = ?`, response.WorkIDs[0]).Scan(&approvedBy); err != nil {
		t.Fatal(err)
	}
	if approvedBy != "" {
		t.Fatalf("approved_by = %q, want empty; pre-approval records no approver", approvedBy)
	}
	var runSource string
	var preApproved int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT source, pre_approved FROM runs WHERE id = ?`, response.RunID,
	).Scan(&runSource, &preApproved); err != nil {
		t.Fatal(err)
	}
	if runSource != "orchestrator" {
		t.Fatalf("runs.source = %q, want orchestrator", runSource)
	}
	if preApproved != 1 {
		t.Fatalf("runs.pre_approved = %d, want 1", preApproved)
	}
}

// Regression for the Critical review finding: tasks.name_key is UNIQUE, and
// admission gives every Run its own Task under the hood. Titles repeat
// constantly on the cockpit and GitHub paths ("Update dependencies", "Fix
// flaky test"), so a second admission sharing a title with an earlier one
// must still succeed and must still produce its own, distinct Run.
func TestRepeatedTitleDoesNotBreakAdmission(t *testing.T) {
	store := newTestStore(t)
	first, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"11000000-0000-4000-8000-000000000006")
	if err != nil {
		t.Fatalf("first admission with a shared title failed: %v", err)
	}
	second, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"11000000-0000-4000-8000-000000000007")
	if err != nil {
		t.Fatalf("second admission with the same title failed: %v", err)
	}
	if first.RunID == second.RunID {
		t.Fatal("two distinct admissions collapsed onto one run")
	}
	var runs int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}
}

// Regression: GitHub delivers repository identities in canonical case
// ("owner/Repo"), but repositories.remote_identity is stored lowercased by
// CreateManagedRepository. Admission must normalize the same way before
// matching, or a case difference alone returns repository_not_found for a
// repository that plainly is managed.
func TestAdmissionMatchesRepositoryIdentityCaseInsensitively(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, "github.com/Example/CaseSensitive")
	_, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: "11000000-0000-4000-8000-000000000008",
		Repository: "github.com/Example/CaseSensitive",
		Name:       "Case sensitivity regression",
		Spec:       "spec text",
		Runtime:    "claude-code",
		Source:     protocol.WorkSourceCockpit,
	})
	if err != nil {
		t.Fatalf("admission with the exact registered case failed: %v", err)
	}
}

// INV-1, the teeth
func TestNonOrchestratorCannotSelfApprove(t *testing.T) {
	store := newTestStore(t)
	for index, source := range []protocol.WorkSource{
		protocol.WorkSourceCockpit, protocol.WorkSourceGitHub,
	} {
		key := fmt.Sprintf("11000000-0000-4000-8000-00000000010%d", index)
		if _, err := admitForTest(t, store, source, true, key); err == nil {
			t.Fatalf("source %q was allowed to set pre_approved", source)
		}
	}
}

func TestUnknownSourceIsRejected(t *testing.T) {
	store := newTestStore(t)
	if _, err := admitForTest(t, store, protocol.WorkSource("wherever"), false,
		"11000000-0000-4000-8000-000000000004"); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}

// AC-3
func TestSameRequestKeyCreatesOneRun(t *testing.T) {
	store := newTestStore(t)
	key := "11000000-0000-4000-8000-000000000005"
	first, err := admitForTest(t, store, protocol.WorkSourceCockpit, false, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := admitForTest(t, store, protocol.WorkSourceCockpit, false, key)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != second.RunID {
		t.Fatalf("run ids %s and %s differ; admission is not idempotent", first.RunID, second.RunID)
	}
	// One Work record, not two: the replay must return the original session,
	// not a second one materialized under the same key.
	if len(first.WorkIDs) != 1 || len(second.WorkIDs) != 1 {
		t.Fatalf("work ids = %d then %d, want 1 each", len(first.WorkIDs), len(second.WorkIDs))
	}
	if first.WorkIDs[0] != second.WorkIDs[0] {
		t.Fatalf("work ids %s and %s differ; the replay created a second Work",
			first.WorkIDs[0], second.WorkIDs[0])
	}
	var runs int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
}

// Regression for the Important finding raised on the Critical fix itself:
// the unique-name suffix (11 runes: " (" + 8 hex + ")") must not push a
// previously valid 200 rune title over normalizeTask's own 200 rune limit.
// A name at exactly the limit is admitted, and the resulting task name is
// exactly 200 runes with the suffix intact, proving the base was truncated
// rather than the whole submission being rejected.
func TestLongNameIsTruncatedToFitTheSuffix(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, "github.com/example/long-name")
	requestKey := "11000000-0000-4000-8000-000000000009"
	name := strings.Repeat("a", 200)
	response, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: requestKey,
		Repository: repository.RemoteIdentity,
		Name:       name,
		Spec:       "spec text",
		Runtime:    "claude-code",
		Source:     protocol.WorkSourceCockpit,
	})
	if err != nil {
		t.Fatalf("admission with a 200 rune name failed: %v", err)
	}
	var taskName string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT name FROM tasks WHERE id = ?`, response.TaskID).Scan(&taskName); err != nil {
		t.Fatal(err)
	}
	if got := utf8.RuneCountInString(taskName); got != 200 {
		t.Fatalf("task name = %d runes, want 200", got)
	}
	digest := sha256.Sum256([]byte(requestKey))
	suffix := " (" + hex.EncodeToString(digest[:])[:8] + ")"
	want := name[:200-utf8.RuneCountInString(suffix)] + suffix
	if taskName != want {
		t.Fatalf("task name = %q, want %q", taskName, want)
	}
}

// Regression: a multibyte name near the limit must be truncated by rune, not
// by byte, or slicing lands mid-character and corrupts the name. 200 runes
// of a 3 byte character (600 bytes) forces truncation, since the base plus
// the 11 rune suffix would otherwise be 211 runes.
func TestMultibyteLongNameTruncatesCleanly(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, "github.com/example/multibyte-name")
	requestKey := "11000000-0000-4000-8000-00000000000a"
	name := strings.Repeat("中", 200)
	response, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: requestKey,
		Repository: repository.RemoteIdentity,
		Name:       name,
		Spec:       "spec text",
		Runtime:    "claude-code",
		Source:     protocol.WorkSourceCockpit,
	})
	if err != nil {
		t.Fatalf("admission with a 200 rune multibyte name failed: %v", err)
	}
	var taskName string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT name FROM tasks WHERE id = ?`, response.TaskID).Scan(&taskName); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(taskName) {
		t.Fatalf("task name is not valid UTF-8: %q", taskName)
	}
	if strings.ContainsRune(taskName, utf8.RuneError) {
		t.Fatalf("task name contains a replacement character, byte slicing corrupted it: %q", taskName)
	}
	if got := utf8.RuneCountInString(taskName); got != 200 {
		t.Fatalf("task name = %d runes, want 200", got)
	}
}

const adoptionSpec = "## Add a farewell function\n\nDone when farewell('world') returns Goodbye, world!"

// orphanTaskForTest reproduces the state a transient failure inside admitTask
// leaves behind: CreateTask committed its own transaction, the run insert
// never happened, so runs.request_key carries nothing and the replay check
// cannot see this submission at all. The task name is admission's exact
// deterministic one, which is what poisons every later retry of the key.
func orphanTaskForTest(
	t *testing.T, store *Store, repositoryID, requestKey, name, prompt string,
) protocol.Task {
	t.Helper()
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name:             admissionTaskName(requestKey, name),
		Prompt:           prompt,
		Runtime:          admissionRuntime,
		TimeoutSeconds:   defaultAdmissionTimeoutSeconds,
		ConcurrencyLimit: 1,
		RepositoryIDs:    []string{repositoryID},
		Schedule:         protocol.TaskSchedule{Enabled: false},
		OutcomeContract:  protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// A request key must not be permanently poisoned by a partial admission.
// uniqueName is deterministic in the request key and tasks.name_key is
// UNIQUE, so before this fix every retry of the key hit task_name_conflict
// from CreateTask, and the run whose request_key would have triggered the
// replay was never written. The orphan is adopted instead, and the retry
// completes the admission it was always meant to complete.
func TestAdmissionAdoptsATaskOrphanedByAPartialFailure(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	requestKey := "11000000-0000-4000-8000-00000000000b"
	orphan := orphanTaskForTest(
		t, store, repository.ID, requestKey, "Add a farewell function", adoptionSpec)

	response, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: requestKey,
		Repository: repository.RemoteIdentity,
		Name:       "Add a farewell function",
		Spec:       adoptionSpec,
		Runtime:    admissionRuntime,
		Source:     protocol.WorkSourceCockpit,
	})
	if err != nil {
		t.Fatalf("retry after a partial admission failed: %v", err)
	}
	if response.TaskID != orphan.ID {
		t.Fatalf("task id = %s, want the adopted orphan %s", response.TaskID, orphan.ID)
	}
	if len(response.WorkIDs) != 1 {
		t.Fatalf("work ids = %d, want 1", len(response.WorkIDs))
	}
	// No duplicate task was created alongside the orphan, and exactly one
	// Run now exists for the key.
	assertCount(t, store, `SELECT COUNT(*) FROM tasks WHERE name_key = ?`,
		normalizeTitleKey(admissionTaskName(requestKey, "Add a farewell function")), 1)
	assertCount(t, store, `SELECT COUNT(*) FROM runs WHERE request_key = ?`,
		admissionRequestKeyPrefix+requestKey, 1)
}

// The suffix is 8 hex characters of a sha256, so adoption cannot assume a
// name match means an identity match. A task carrying a different spec under
// a colliding name must be refused with its own error rather than silently
// running the wrong spec.
func TestAdmissionRefusesToAdoptATaskCarryingADifferentSpec(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	requestKey := "11000000-0000-4000-8000-00000000000c"
	orphanTaskForTest(
		t, store, repository.ID, requestKey, "Add a farewell function", "a completely different spec")

	_, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: requestKey,
		Repository: repository.RemoteIdentity,
		Name:       "Add a farewell function",
		Spec:       adoptionSpec,
		Runtime:    admissionRuntime,
		Source:     protocol.WorkSourceCockpit,
	})
	if !serviceErrorCode(err, "admission_name_collision") {
		t.Fatalf("colliding-name admission error = %v, want admission_name_collision", err)
	}
	// Nothing ran under the colliding name.
	assertCount(t, store, `SELECT COUNT(*) FROM runs WHERE request_key = ?`,
		admissionRequestKeyPrefix+requestKey, 0)
}

// The same guard applies to the runtime: a colliding task that would run the
// spec on a different agent runtime is not this submission's task either.
func TestAdmissionRefusesToAdoptATaskCarryingADifferentRuntime(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	requestKey := "11000000-0000-4000-8000-00000000000d"
	orphanTaskForTest(
		t, store, repository.ID, requestKey, "Add a farewell function", adoptionSpec)

	_, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: requestKey,
		Repository: repository.RemoteIdentity,
		Name:       "Add a farewell function",
		Spec:       adoptionSpec,
		Runtime:    protocol.RuntimeCodex,
		Source:     protocol.WorkSourceCockpit,
	})
	if !serviceErrorCode(err, "admission_name_collision") {
		t.Fatalf("colliding-runtime admission error = %v, want admission_name_collision", err)
	}
}

func assertCount(t *testing.T, store *Store, query string, argument any, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(context.Background(), query, argument).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count = %d, want %d (%s)", got, want, query)
	}
}
