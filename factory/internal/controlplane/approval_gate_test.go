package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// INV-1: the approval gate has to survive cancel-then-retry and
// cancel-then-replace, not only the direct draft transitions. A draft that
// was cancelled is 'cancelled', so the retry and replace paths both see an
// ordinary terminal Session and would happily requeue a spec no human ever
// read. These tests hold the gate on the only two paths that can reopen
// terminal Work.

// forceSessionFailed drives one Session to a terminal failed state without
// running an Attempt, the same shortcut the existing retry guard tests use.
func forceSessionFailed(t *testing.T, store *Store, workID string) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE sessions
		SET state = 'failed', terminal_at = 1, failure_reason = 'forced',
		    assigned_worker_id = NULL, execution_owner = 'none'
		WHERE id = ?
	`, workID); err != nil {
		t.Fatal(err)
	}
}

// assertNeverApproved proves a refusal left no trace: the Session is not
// dispatchable and still carries no approval record.
func assertNeverApproved(t *testing.T, store *Store, workID string) {
	t.Helper()
	var state, approvedBy string
	var approvedAt *int64
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT state, approved_by, approved_at FROM sessions WHERE id = ?`, workID,
	).Scan(&state, &approvedBy, &approvedAt); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" {
		t.Fatalf("state = %q, want cancelled; a never-approved Session was reopened", state)
	}
	if approvedBy != "" {
		t.Fatalf("approved_by = %q, want empty", approvedBy)
	}
	if approvedAt != nil {
		t.Fatalf("approved_at = %d, want NULL", *approvedAt)
	}
	var queuedExecutions int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM executions WHERE session_id = ? AND state = 'queued'`, workID,
	).Scan(&queuedExecutions); err != nil {
		t.Fatal(err)
	}
	if queuedExecutions != 0 {
		t.Fatalf("queued executions = %d, want 0; a never-approved spec is claimable", queuedExecutions)
	}
}

func TestCancelledDraftCannotBeRetried(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	eligibleWorkerForAdmission(t, store, "worker-unapproved-retry")
	admission := admitDraftForTest(t, store)
	workID := admission.WorkIDs[0]

	if _, err := store.CancelSession(ctx, admission.RunID, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetrySession(ctx, admission.RunID, workID); !serviceErrorCode(err, "work_not_approved") {
		t.Fatalf("retry of a cancelled draft error = %v, want work_not_approved", err)
	}
	assertNeverApproved(t, store, workID)
}

func TestCancelledDraftRetryIsRefusedOverHTTP(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	eligibleWorkerForAdmission(t, store, "worker-unapproved-retry-http")
	admission := admitDraftForTest(t, store)
	workID := admission.WorkIDs[0]
	if _, err := store.CancelSession(ctx, admission.RunID, workID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		server.URL+"/api/v1/work/"+workID+"/retry", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var errorBody protocol.ErrorBody
	if err := json.NewDecoder(response.Body).Decode(&errorBody); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusConflict || errorBody.Error.Code != "work_not_approved" {
		t.Fatalf("retry response = %d %#v, want 409 work_not_approved", response.StatusCode, errorBody.Error)
	}
	assertNeverApproved(t, store, workID)
}

func TestCancelledDraftCannotBeReplaced(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	eligibleWorkerForAdmission(t, store, "worker-unapproved-replace")
	admission := admitDraftForTest(t, store)
	workID := admission.WorkIDs[0]
	if _, err := store.CancelSession(ctx, admission.RunID, workID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ReplaceWork(ctx, protocol.ReplaceWorkRequest{
		RequestKey: "replace-unapproved-draft", WorkID: workID,
	}); !serviceErrorCode(err, "work_not_approved") {
		t.Fatalf("replace of a cancelled draft error = %v, want work_not_approved", err)
	}
	assertNeverApproved(t, store, workID)
	var successors int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE predecessor_work_id = ?`, workID).Scan(&successors); err != nil {
		t.Fatal(err)
	}
	if successors != 0 {
		t.Fatalf("successor Sessions = %d, want 0", successors)
	}
}

// The guard must be narrow enough that an approval, once recorded, keeps
// working for the whole life of the Work.
func TestApprovedWorkRemainsRetryableAfterFailure(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	eligibleWorkerForAdmission(t, store, "worker-approved-retry")
	admission := admitDraftForTest(t, store)
	workID := admission.WorkIDs[0]
	if _, err := store.ApproveWork(ctx, workID,
		protocol.ApproveWorkRequest{Actor: "octocat"}); err != nil {
		t.Fatal(err)
	}
	forceSessionFailed(t, store, workID)

	if _, err := store.RetrySession(ctx, admission.RunID, workID); err != nil {
		t.Fatalf("approved Work could not be retried: %v", err)
	}
	assertQueued(t, store, workID)
}

// runs.pre_approved = 1 means the gate was satisfied at admission, so an
// orchestrator submission never needs an approval record to be retryable.
func TestPreApprovedWorkRemainsRetryable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	eligibleWorkerForAdmission(t, store, "worker-preapproved-retry")
	admission, err := admitForTest(t, store, protocol.WorkSourceOrchestrator, true,
		"12000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	workID := admission.WorkIDs[0]
	forceSessionFailed(t, store, workID)

	if _, err := store.RetrySession(ctx, admission.RunID, workID); err != nil {
		t.Fatalf("pre-approved Work could not be retried: %v", err)
	}
	assertQueued(t, store, workID)
}

// The regression the naive predicate causes: every Run that predates
// admission has pre_approved = 0 and approved_at NULL, and none of them ever
// had an approval gate to satisfy.
func TestOrdinaryRunRemainsRetryable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	detail := runTaskForTest(t, store, "approval-gate-ordinary")
	workID := detail.Sessions[0].ID
	forceSessionFailed(t, store, workID)

	if _, err := store.RetrySession(ctx, detail.Run.ID, workID); err != nil {
		t.Fatalf("an ordinary manual Run could not be retried: %v", err)
	}
	assertQueued(t, store, workID)
}

// A scheduled Run reaches the same predicate through a different source.
func TestScheduledRunRemainsRetryable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	detail := runTaskForTest(t, store, "approval-gate-scheduled")
	workID := detail.Sessions[0].ID
	if _, err := store.db.ExecContext(ctx,
		`UPDATE runs SET source = 'schedule' WHERE id = ?`, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	forceSessionFailed(t, store, workID)

	if _, err := store.RetrySession(ctx, detail.Run.ID, workID); err != nil {
		t.Fatalf("a scheduled Run could not be retried: %v", err)
	}
	assertQueued(t, store, workID)
}
