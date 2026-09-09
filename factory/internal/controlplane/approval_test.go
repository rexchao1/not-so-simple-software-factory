package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// AC-2 and the dispatching half of the gate: with a worker that can actually
// take the Work, approval must reach queued with an execution assigned to
// that worker. Asserting only "not draft" would pass on a blocked Session and
// leave ApproveWork's queueExistingExecution branch unexercised.
func TestApproveMovesDraftOutOfDraft(t *testing.T) {
	store := newTestStore(t)
	worker := eligibleWorkerForAdmission(t, store, "worker-approve")
	admission := admitDraftForTest(t, store)

	work, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "octocat"})
	if err != nil {
		t.Fatal(err)
	}
	if work.State != protocol.SessionQueued {
		t.Fatalf("state = %q, want queued", work.State)
	}
	if work.AssignedWorkerID != worker.ID {
		t.Fatalf("assigned_worker_id = %q, want %q", work.AssignedWorkerID, worker.ID)
	}
	assertQueued(t, store, admission.WorkIDs[0])
	if work.ApprovedBy != "octocat" {
		t.Fatalf("approved_by = %q, want octocat", work.ApprovedBy)
	}
	if work.ApprovedAt == nil {
		t.Fatal("approved_at was not recorded")
	}
	if work.Delivery != protocol.DeliveryPullRequest {
		t.Fatalf("delivery = %q, want %q", work.Delivery, protocol.DeliveryPullRequest)
	}
}

func TestApproveRequiresAnActor(t *testing.T) {
	store := newTestStore(t)
	admission := admitDraftForTest(t, store)
	if _, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "  "}); err == nil {
		t.Fatal("approval without an actor was accepted")
	}
}

func TestApproveRejectsNonDraft(t *testing.T) {
	store := newTestStore(t)
	eligibleWorkerForAdmission(t, store, "worker-approve-twice")
	admission := admitDraftForTest(t, store)
	if _, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "octocat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "octocat"}); err == nil {
		t.Fatal("approving an already approved session was accepted")
	}
}

// INV-10: once queued, no approval gate remains.
func TestApprovedWorkHasNoRemainingGate(t *testing.T) {
	store := newTestStore(t)
	eligibleWorkerForAdmission(t, store, "worker-gate")
	admission := admitDraftForTest(t, store)
	if _, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "octocat"}); err != nil {
		t.Fatal(err)
	}
	var drafts int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sessions WHERE run_id = ? AND state = 'draft'`,
		admission.RunID).Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	if drafts != 0 {
		t.Fatalf("drafts remaining after approval = %d, want 0", drafts)
	}
	// The gate is gone because the Work is dispatchable, not because it
	// stalled somewhere no second approval can reach it.
	assertQueued(t, store, admission.WorkIDs[0])
}

// FINDING 1: the actor field must be bounded to 255 bytes, matching the
// sessions.approved_by CHECK (length(CAST(approved_by AS BLOB)) <= 255).
// Without an application-level bound, an over-long actor fails the UPDATE's
// CHECK constraint and surfaces as a generic 503 instead of a 400.
func TestApproveRejectsActorOverByteLimit(t *testing.T) {
	store := newTestStore(t)
	eligibleWorkerForAdmission(t, store, "worker-approve-actor-long")
	admission := admitDraftForTest(t, store)

	actor := strings.Repeat("a", 256)
	_, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: actor})
	if err == nil {
		t.Fatal("approval with a 256-byte actor was accepted")
	}
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Status != 400 || serviceErr.Code != "invalid_actor" {
		t.Fatalf("err = %#v, want 400 invalid_actor", err)
	}
}

func TestApproveAcceptsActorAtByteLimit(t *testing.T) {
	store := newTestStore(t)
	eligibleWorkerForAdmission(t, store, "worker-approve-actor-max")
	admission := admitDraftForTest(t, store)

	actor := strings.Repeat("a", 255)
	work, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if work.ApprovedBy != actor {
		t.Fatalf("approved_by = %q, want the 255-byte actor", work.ApprovedBy)
	}
	assertQueued(t, store, admission.WorkIDs[0])
}

// FINDING 2: approval must still succeed, and must still record who approved
// it and when, even when no worker is eligible yet. Approval and worker
// assignment are separate concerns; the human gate closes regardless of
// whether the scheduler can act on it immediately.
func TestApproveWithNoEligibleWorkerLandsBlocked(t *testing.T) {
	store := newTestStore(t)
	admission := admitDraftForTest(t, store)

	work, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "octocat"})
	if err != nil {
		t.Fatal(err)
	}
	if work.State != protocol.SessionBlocked {
		t.Fatalf("state = %q, want blocked", work.State)
	}
	if work.BlockedReason == "" {
		t.Fatal("blocked_reason was not recorded")
	}
	if work.ApprovedBy != "octocat" {
		t.Fatalf("approved_by = %q, want octocat", work.ApprovedBy)
	}
	if work.ApprovedAt == nil {
		t.Fatal("approved_at was not recorded even though the work is blocked")
	}
	// The negative mirror of assertQueued: nothing was queued for a Worker
	// that cannot take it.
	var executions int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM executions WHERE session_id = ?`, admission.WorkIDs[0],
	).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if executions != 0 {
		t.Fatalf("executions = %d, want 0 for a blocked approval", executions)
	}
}
