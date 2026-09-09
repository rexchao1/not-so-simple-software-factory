package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// admitPlanningOptions is the small set of things these tests vary. Everything
// else is the same submission the light-factory skill makes: a pre-approved
// orchestrator submission of a PRD on the Critique Pipeline, against the
// project's registered repository, on claude-code.
type admitPlanningOptions struct {
	RequestKey string
	PipelineID string
	Runtime    string
	Planning   *protocol.WorkPlanning
}

func admitPlanningWorkForTest(
	t *testing.T, store *Store, options admitPlanningOptions,
) (protocol.AdmitWorkResponse, error) {
	t.Helper()
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	if options.PipelineID == "" {
		options.PipelineID = protocol.CritiquePipelineID
	}
	if options.Runtime == "" {
		options.Runtime = admissionRuntime
	}
	return firstTwo(store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey:  options.RequestKey,
		Repository:  repository.RemoteIdentity,
		Name:        "Critique payer checkpoint 2",
		Spec:        "# Checkpoint 2: the dashboard finishes a parked live payer\n",
		Runtime:     options.Runtime,
		Source:      protocol.WorkSourceOrchestrator,
		PreApproved: true,
		PipelineID:  options.PipelineID,
		Planning:    options.Planning,
	}))
}

func planningTripleForTest() *protocol.WorkPlanning {
	return &protocol.WorkPlanning{Project: "payer", Number: 2, Round: 1}
}

// storedPlanningTriple reads the three columns straight off the session row,
// so these tests prove what admission wrote rather than what a loader chose to
// surface.
func storedPlanningTriple(t *testing.T, store *Store, workID string) (string, int, int) {
	t.Helper()
	var project sql.NullString
	var number, round sql.NullInt64
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT planning_project, planning_number, planning_round FROM sessions WHERE id = ?
	`, workID).Scan(&project, &number, &round); err != nil {
		t.Fatal(err)
	}
	if !project.Valid && !number.Valid && !round.Valid {
		return "", 0, 0
	}
	if !project.Valid || !number.Valid || !round.Valid {
		t.Fatalf("a half-written planning triple: %#v %#v %#v", project, number, round)
	}
	return project.String, int(number.Int64), int(round.Int64)
}

func TestPlanningAdmissionStoresTheTripleOnTheWork(t *testing.T) {
	store := newTestStore(t)
	response, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: "52000000-0000-4000-8000-000000000001",
		Planning:   planningTripleForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.WorkIDs) != 1 {
		t.Fatalf("work ids = %v, want one", response.WorkIDs)
	}
	project, number, round := storedPlanningTriple(t, store, response.WorkIDs[0])
	if project != "payer" || number != 2 || round != 1 {
		t.Fatalf("stored triple = %q %d %d, want payer 2 1", project, number, round)
	}
	if response.State != protocol.SessionQueued && response.State != protocol.SessionBlocked {
		t.Fatalf("state = %q, want a pre-approved submission out of draft", response.State)
	}
}

// A project id is folded the way the planning API folds it, or a pass admitted
// for "Payer" would join nothing while its checkpoint sat under "payer".
func TestPlanningAdmissionFoldsTheProjectLikeThePlanningAPI(t *testing.T) {
	store := newTestStore(t)
	response, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: "52000000-0000-4000-8000-000000000002",
		Planning:   &protocol.WorkPlanning{Project: "  Payer  ", Number: 2, Round: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	project, _, round := storedPlanningTriple(t, store, response.WorkIDs[0])
	if project != "payer" || round != 3 {
		t.Fatalf("stored project and round = %q %d, want payer 3", project, round)
	}
}

// Ordinary Work carries no triple at all, so "planning_project IS NOT NULL" is
// a complete test for whether a session is a pass.
func TestOrdinaryWorkCarriesNoPlanningTriple(t *testing.T) {
	store := newTestStore(t)
	response, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"52000000-0000-4000-8000-000000000003")
	if err != nil {
		t.Fatal(err)
	}
	if project, number, round := storedPlanningTriple(t, store, response.WorkIDs[0]); project != "" ||
		number != 0 || round != 0 {
		t.Fatalf("ordinary Work carries a planning triple: %q %d %d", project, number, round)
	}
}

// Each rejection is its own code, because the light-factory scripts branch on
// the code and a single invalid_planning would tell them nothing.
func TestPlanningAdmissionRejections(t *testing.T) {
	tests := []struct {
		name       string
		planning   *protocol.WorkPlanning
		pipelineID string
		code       string
		status     int
	}{
		{
			name: "no project", planning: &protocol.WorkPlanning{Number: 2, Round: 1},
			code: "invalid_planning_project", status: 400,
		},
		{
			name:     "project is not a slug",
			planning: &protocol.WorkPlanning{Project: "two--dashes", Number: 2, Round: 1},
			code:     "invalid_planning_project", status: 400,
		},
		{
			name: "no number", planning: &protocol.WorkPlanning{Project: "payer", Round: 1},
			code: "invalid_planning_number", status: 400,
		},
		{
			name:     "number below one",
			planning: &protocol.WorkPlanning{Project: "payer", Number: 0, Round: 1},
			code:     "invalid_planning_number", status: 400,
		},
		{
			name: "no round", planning: &protocol.WorkPlanning{Project: "payer", Number: 2},
			code: "invalid_planning_round", status: 400,
		},
		{
			name:     "round below one",
			planning: &protocol.WorkPlanning{Project: "payer", Number: 2, Round: -1},
			code:     "invalid_planning_round", status: 400,
		},
		{
			name: "another Pipeline", planning: planningTripleForTest(),
			pipelineID: protocol.DefaultPipelineID,
			code:       "planning_pipeline_required", status: 400,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			_, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
				RequestKey: "52000000-0000-4000-8000-00000000010" + string(rune('0'+index)),
				PipelineID: test.pipelineID,
				Planning:   test.planning,
			})
			var serviceErr *ServiceError
			if !errors.As(err, &serviceErr) || serviceErr.Code != test.code ||
				serviceErr.Status != test.status {
				t.Fatalf("admission = %#v, want %d %s", err, test.status, test.code)
			}
		})
	}
}

// A rejected triple must leave nothing behind, or the request key that failed
// validation would be poisoned by the Task the admission created before it.
func TestRejectedPlanningAdmissionCreatesNoWork(t *testing.T) {
	store := newTestStore(t)
	_, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: "52000000-0000-4000-8000-000000000004",
		Planning:   &protocol.WorkPlanning{Project: "payer", Number: 2},
	})
	requireServiceError(t, err, "invalid_planning_round")
	var runs int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("a refused planning admission left %d Runs behind", runs)
	}
}

// AC-3 for a planning submission: the light factory retries a submit whose
// response it never saw, and must get the same pass back rather than a second
// critique on the same round.
func TestPlanningAdmissionReplaysThroughTheRequestKey(t *testing.T) {
	store := newTestStore(t)
	options := admitPlanningOptions{
		RequestKey: "52000000-0000-4000-8000-000000000005",
		Planning:   planningTripleForTest(),
	}
	first, err := admitPlanningWorkForTest(t, store, options)
	if err != nil {
		t.Fatal(err)
	}
	second, created, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey:  options.RequestKey,
		Repository:  admissionRepositoryIdentity,
		Name:        "Critique payer checkpoint 2",
		Spec:        "# Checkpoint 2: the dashboard finishes a parked live payer\n",
		Runtime:     admissionRuntime,
		Source:      protocol.WorkSourceOrchestrator,
		PreApproved: true,
		PipelineID:  protocol.CritiquePipelineID,
		Planning:    &protocol.WorkPlanning{Project: "payer", Number: 2, Round: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("a replayed request key reported a new admission")
	}
	if second.RunID != first.RunID || len(second.WorkIDs) != 1 ||
		second.WorkIDs[0] != first.WorkIDs[0] {
		t.Fatalf("replay = %#v, want the original %#v", second, first)
	}
	var passes int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sessions WHERE planning_project IS NOT NULL`).Scan(&passes); err != nil {
		t.Fatal(err)
	}
	if passes != 1 {
		t.Fatalf("the replay created %d passes, want one", passes)
	}
}

// A critique reports by exiting. agent_update would hold the Work open waiting
// for a factory update call naming a pull request the critic cannot open.
func TestCritiqueWorkIsAdmittedAsProcessExit(t *testing.T) {
	store := newTestStore(t)
	response, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: "52000000-0000-4000-8000-000000000006",
		Planning:   planningTripleForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Run(context.Background(), response.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.OutcomeContract != protocol.OutcomeProcessExit {
		t.Fatalf("outcome contract = %q, want process_exit", run.Run.OutcomeContract)
	}
	ordinary, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"52000000-0000-4000-8000-000000000007")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Run(context.Background(), ordinary.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if other.Run.OutcomeContract != protocol.OutcomeAgentUpdate {
		t.Fatalf("ordinary Work outcome contract = %q, want agent_update untouched",
			other.Run.OutcomeContract)
	}
}
