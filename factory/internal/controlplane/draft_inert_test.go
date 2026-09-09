package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const inertRepositoryIdentity = "github.com/example/inert"

// inertTaskSnapshot is the minimum a Run snapshot needs for the claim path to
// read it back. The earlier '{}' was enough only because nothing in this file
// ever reached a successful claim.
const inertTaskSnapshot = `{"name":"Inert draft","prompt":"spec text",` +
	`"concurrency_limit":1,"outcome_contract":"process_exit",` +
	`"timeout_seconds":3600,"runtime":"claude-code"}`

// draftSession inserts one run and one draft session directly, so this task
// does not depend on the admission endpoint that Task 3 adds. These are
// store-level tests: they assert how a draft behaves, not how it was created.
func draftSession(t *testing.T, store *Store) (runID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	repository := registerTestRepository(t, store, inertRepositoryIdentity)
	taskID := seedTaskForTest(t, store, repository.ID)
	runID = "33000000-0000-4000-8000-000000000001"
	sessionID = "33000000-0000-4000-8000-000000000002"
	now := time.Now().UTC().UnixMilli()

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO runs(id, request_key, request_digest, task_id, task_snapshot,
		                 source, admitted_at, updated_at, pre_approved)
		VALUES (?, ?, X'00', ?, ?, 'cockpit', ?, ?, 0)
	`, runID, "inert-"+runID, taskID, inertTaskSnapshot, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO sessions(id, run_id, repository_id, repository_identity,
		                     resolved_prompt, required_runtime, timeout_seconds,
		                     state, admitted_at, delivery)
		VALUES (?, ?, ?, ?, 'spec text', 'claude-code', 3600, 'draft', ?, 'pr')
	`, sessionID, runID, repository.ID, repository.RemoteIdentity, now); err != nil {
		t.Fatal(err)
	}
	return runID, sessionID
}

// TestDraftIsNotClaimable needs a worker that could otherwise take this
// Session, or it proves nothing: an unroutable worker returns an empty claim
// for every Session in the database, draft or not. The second half is the
// control. It flips the one field under test, leaving repository, runtime and
// worker identical, and shows the same claim call now succeeds. Only the
// draft state can explain the difference.
func TestDraftIsNotClaimable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	worker := eligibleWorkerFor(t, store, "worker-draft", "inert", inertRepositoryIdentity, protocol.RuntimeClaudeCode)
	_, sessionID := draftSession(t, store)

	claim, err := store.Claim(ctx, worker.ID, claimRequestForTest())
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		t.Fatalf("a draft session was claimed: %s", claim.Session.ID)
	}
	var state string
	if err := store.db.QueryRowContext(ctx,
		`SELECT state FROM sessions WHERE id = ?`, sessionID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "draft" {
		t.Fatalf("session state = %q, want draft", state)
	}

	if _, err := store.db.ExecContext(ctx,
		`UPDATE sessions SET state = 'blocked' WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}
	control, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{
		RequestID: "draft-inert-control", LeaseToken: tokenA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if control == nil || control.Session.ID != sessionID {
		t.Fatalf("control claim = %#v, want the same Session once it is no longer a draft", control)
	}
}

func TestDraftDoesNotMarkRunTerminal(t *testing.T) {
	store := newTestStore(t)
	runID, _ := draftSession(t, store)
	var terminalAt *int64
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT terminal_at FROM runs WHERE id = ?`, runID).Scan(&terminalAt); err != nil {
		t.Fatal(err)
	}
	if terminalAt != nil {
		t.Fatalf("runs.terminal_at = %d for a draft-only run, want NULL", *terminalAt)
	}
}

func TestDraftOnlyRunReportsDraftState(t *testing.T) {
	store := newTestStore(t)
	runID, _ := draftSession(t, store)
	detail, err := store.Run(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.State != "draft" {
		t.Fatalf("run state = %q, want draft", detail.Run.State)
	}
}

// TestDraftIsCancellable proves a draft can actually be removed. Rejecting
// and editing a draft are out of scope for this phase, so cancellation is
// the only way to get rid of one. CancelSession is the store method the
// cockpit's per-session cancel route calls.
func TestDraftIsCancellable(t *testing.T) {
	store := newTestStore(t)
	runID, sessionID := draftSession(t, store)

	if _, err := store.CancelSession(context.Background(), runID, sessionID); err != nil {
		t.Fatal(err)
	}

	var state string
	var terminalAt *int64
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT state, terminal_at FROM sessions WHERE id = ?`, sessionID,
	).Scan(&state, &terminalAt); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" {
		t.Fatalf("session state = %q, want cancelled", state)
	}
	if terminalAt == nil {
		t.Fatal("sessions.terminal_at is NULL for a cancelled session, want set")
	}
}
