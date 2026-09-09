package controlplane

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestSetManagedRepositoryDefaultDeliveryRoundTrips(t *testing.T) {
	store := newTestStore(t)
	repository, _, err := store.CreateManagedRepository(t.Context(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: "github.com/octocat/factory-scratch"})
	if err != nil {
		t.Fatal(err)
	}
	if repository.DefaultDelivery != protocol.DeliveryPullRequest {
		t.Fatalf("a new repository defaulted to %q, want pr", repository.DefaultDelivery)
	}

	updated, err := store.SetManagedRepositoryDefaultDelivery(t.Context(),
		repository.ID, protocol.DeliveryPullRequestAutoMerge)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DefaultDelivery != protocol.DeliveryPullRequestAutoMerge {
		t.Fatalf("delivery = %q, want pr+automerge", updated.DefaultDelivery)
	}

	reread, err := store.ManagedRepository(t.Context(), repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.DefaultDelivery != protocol.DeliveryPullRequestAutoMerge {
		t.Fatal("the setting did not survive a re-read")
	}

	// And back off again. Turning auto-merge off must always be possible.
	back, err := store.SetManagedRepositoryDefaultDelivery(t.Context(),
		repository.ID, protocol.DeliveryPullRequest)
	if err != nil {
		t.Fatal(err)
	}
	if back.DefaultDelivery != protocol.DeliveryPullRequest {
		t.Fatalf("delivery = %q, want pr", back.DefaultDelivery)
	}
}

func TestSetManagedRepositoryDefaultDeliveryRejectsAnUnknownMode(t *testing.T) {
	store := newTestStore(t)
	repository, _, err := store.CreateManagedRepository(t.Context(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: "github.com/octocat/factory-scratch"})
	if err != nil {
		t.Fatal(err)
	}
	// Validated before the UPDATE, so a bad value is a 400 rather than a
	// SQLite CHECK violation surfacing as a 500.
	if _, err := store.SetManagedRepositoryDefaultDelivery(t.Context(),
		repository.ID, protocol.DeliveryMode("automerge-please")); !serviceErrorCode(err, "invalid_repository") {
		t.Fatalf("an unknown delivery mode was accepted, error = %v", err)
	}
}

func TestSetManagedRepositoryDefaultDeliveryIsNotFound(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.SetManagedRepositoryDefaultDelivery(t.Context(),
		"11111111-1111-4111-8111-111111111111", protocol.DeliveryPullRequest); err == nil {
		t.Fatal("an unknown repository was accepted")
	}
}

func TestRunTaskTakesTheDeliveryModeFromTheRepository(t *testing.T) {
	// The per-project setting is only worth having if the paths that actually
	// create Work honour it. Admission read repositories.default_delivery from
	// the start; RunTask and the scheduler both hardcoded 'pr', so a project
	// with auto-merge turned on in the cockpit still produced Work that could
	// never merge, and nothing said so.
	ctx := context.Background()
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	repositoryID := worker.Repositories[0].ID
	if _, err := store.SetManagedRepositoryDefaultDelivery(ctx,
		repositoryID, protocol.DeliveryPullRequestAutoMerge); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Delivery inheritance", Prompt: "do the thing", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{repositoryID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "delivery-inheritance"})
	if err != nil {
		t.Fatal(err)
	}

	var delivery string
	if err := store.db.QueryRowContext(ctx,
		`SELECT delivery FROM sessions WHERE id = ?`, run.Sessions[0].ID).Scan(&delivery); err != nil {
		t.Fatal(err)
	}
	if protocol.DeliveryMode(delivery) != protocol.DeliveryPullRequestAutoMerge {
		t.Fatalf("session delivery = %q, want pr+automerge inherited from the repository", delivery)
	}
}

func TestRunTaskDefaultsToPullRequestWhenTheRepositoryDoes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Delivery default", Prompt: "do the thing", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "delivery-default"})
	if err != nil {
		t.Fatal(err)
	}
	var delivery string
	if err := store.db.QueryRowContext(ctx,
		`SELECT delivery FROM sessions WHERE id = ?`, run.Sessions[0].ID).Scan(&delivery); err != nil {
		t.Fatal(err)
	}
	if protocol.DeliveryMode(delivery) != protocol.DeliveryPullRequest {
		t.Fatalf("session delivery = %q, want pr", delivery)
	}
}
