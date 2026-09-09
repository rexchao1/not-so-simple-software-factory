package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	workerA = "worker-a"
	tokenA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokenB  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// admissionRepositoryIdentity is the repository every admitForTest submission
// targets. A worker only routes admitted Work if it advertises this exact
// identity: registerTestRepository makes the repository centrally managed,
// which turns on selectSessionRoute's source-access requirement, and a worker
// with no RepositoryRegistration satisfies neither half of it.
const admissionRepositoryIdentity = "github.com/example/scratch"

// admissionRuntime is the runtime every admitForTest submission requires.
const admissionRuntime = protocol.RuntimeClaudeCode

// eligibleWorkerForAdmission registers a worker that can actually take the
// Work admitForTest admits. Without it every approval lands blocked, and the
// dispatching half of the approval gate goes untested. Two things have to
// line up, and registerTestWorker supplies neither: the repository has to be
// advertised, and the worker's runtime has to match the runtime admitForTest
// requests, or selectSessionRoute drops the candidate on the capability
// check before it ever reaches the source-access one.
func eligibleWorkerForAdmission(t *testing.T, store *Store, id string) protocol.Worker {
	t.Helper()
	return eligibleWorkerFor(t, store, id, "scratch", admissionRepositoryIdentity, admissionRuntime)
}

// eligibleWorkerFor registers a worker that selectSessionRoute will pick for
// one repository identity and runtime.
func eligibleWorkerFor(
	t *testing.T, store *Store, id, repositoryKey, remoteIdentity, runtime string,
) protocol.Worker {
	t.Helper()
	worker, err := store.RegisterWorker(context.Background(), id, protocol.WorkerRegistration{
		Name: id, WorkerVersion: "test", ClaimProtocolVersion: protocol.ClaimProtocolVersion,
		Runtime:        runtime,
		RuntimeVersion: runtime + "-test", Capacity: 4, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: repositoryKey, RemoteIdentity: remoteIdentity,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

// assertQueued is the positive mirror of the draft assertions in
// admission_test.go: a Session that reached queued carries an assigned worker
// and a queued executions row for that worker, so a Claim can dispatch it.
func assertQueued(t *testing.T, store *Store, workID string) {
	t.Helper()
	var state string
	var assignedWorkerID sql.NullString
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT state, assigned_worker_id FROM sessions WHERE id = ?`, workID,
	).Scan(&state, &assignedWorkerID); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("state = %q, want queued", state)
	}
	if !assignedWorkerID.Valid || assignedWorkerID.String == "" {
		t.Fatal("assigned_worker_id is not set, so nothing can claim this Session")
	}
	var executions int
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM executions
		WHERE session_id = ? AND state = 'queued' AND assigned_worker_id = ?
	`, workID, assignedWorkerID.String).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("queued executions for %s = %d, want 1", assignedWorkerID.String, executions)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir()+"/controlplane.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

// requireServiceError fails the test unless err is a *ServiceError carrying
// the given code at status 400, the house shape for a save-time validation
// rejection.
func requireServiceError(t *testing.T, err error, code string) {
	t.Helper()
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Status != 400 || serviceErr.Code != code {
		t.Fatalf("err = %#v, want 400 %s", err, code)
	}
}

func registerTestWorker(
	t *testing.T,
	store *Store,
	id string,
	capacity int,
	repositories ...protocol.RepositoryRegistration,
) protocol.Worker {
	t.Helper()
	worker, err := store.RegisterWorker(context.Background(), id, protocol.WorkerRegistration{
		Name: id, WorkerVersion: "test", ClaimProtocolVersion: protocol.ClaimProtocolVersion,
		Runtime:        protocol.RuntimeCodex,
		RuntimeVersion: "codex-test", Capacity: capacity, Health: "healthy",
		Repositories: repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func registerTestRepository(
	t *testing.T, store *Store, identity string,
) protocol.ManagedRepository {
	t.Helper()
	repository, _, err := store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

// seedTaskForTest creates a minimal task so a run row has a valid task_id.
func seedTaskForTest(t *testing.T, store *Store, repositoryID string) string {
	t.Helper()
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		RequestKey:       "seed-" + repositoryID,
		Name:             "Seed",
		Prompt:           "spec text",
		Runtime:          "claude-code",
		TimeoutSeconds:   3600,
		ConcurrencyLimit: 1,
		RepositoryIDs:    []string{repositoryID},
		Schedule:         protocol.TaskSchedule{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	return task.ID
}

// claimRequestForTest builds a minimal valid claim request. ClaimRequest has
// no runtime field: runtime compatibility is decided by matching the
// session's required_runtime against the worker's advertised capabilities,
// not by anything on the request.
func claimRequestForTest() protocol.ClaimRequest {
	return protocol.ClaimRequest{RequestID: "draft-inert-claim", LeaseToken: tokenA}
}

// firstTwo drops the created boolean from AdmitWork's return so test bodies
// calling it through a single assignment stay readable.
func firstTwo(
	response protocol.AdmitWorkResponse, _ bool, err error,
) (protocol.AdmitWorkResponse, error) {
	return response, err
}
