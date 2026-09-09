package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// readOnlyStagePipeline is one agent stage that may not write, saved through
// the ordinary Pipeline API so the flag is exercised on the path an operator
// uses rather than only on the built-in.
func readOnlyStagePipeline(t *testing.T, store *Store, name string) protocol.Pipeline {
	t.Helper()
	pipeline, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: name,
		Stages: []protocol.PipelineStage{{
			Name: "Read", Kind: protocol.StageKindAgent, Prompt: "{{ task.prompt }}", ReadOnly: true,
		}},
	})
	if err != nil {
		t.Fatalf("save a read-only Pipeline: %v", err)
	}
	return pipeline
}

// A code stage runs an operator's command and a delivery stage runs Factory's
// own git operations. Neither is narrowed by a tool-denial flag, so accepting
// the flag there would report a guarantee nothing keeps.
func TestReadOnlyIsRefusedOnMechanicalStages(t *testing.T) {
	tests := []struct {
		name  string
		stage protocol.PipelineStage
	}{
		{name: "code", stage: protocol.PipelineStage{
			Name: "Check", Kind: protocol.StageKindCode, Command: "make test", ReadOnly: true,
		}},
		{name: "delivery", stage: protocol.PipelineStage{
			Name: "Deliver", Kind: protocol.StageKindDelivery, ReadOnly: true,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			_, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
				Name: "Mechanical " + test.name,
				Stages: []protocol.PipelineStage{
					{Name: "Work", Kind: protocol.StageKindAgent, Prompt: "{{ task.prompt }}"},
					test.stage,
				},
			})
			requireServiceError(t, err, "invalid_pipeline_stage_read_only")
		})
	}
}

// The flag has to survive the save and the read, or an operator would set it,
// see it accepted, and get a stage that writes.
func TestReadOnlySurvivesSaveAndRead(t *testing.T) {
	store := newTestStore(t)
	saved := readOnlyStagePipeline(t, store, "Read only")
	if !saved.Stages[0].ReadOnly {
		t.Fatal("the saved Pipeline came back without its read-only flag")
	}
	reloaded, err := store.Pipeline(context.Background(), saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Stages[0].ReadOnly {
		t.Fatal("a reloaded Pipeline lost its read-only flag")
	}
	page, err := store.Pipelines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, pipeline := range page.Pipelines {
		if pipeline.ID == saved.ID && !pipeline.Stages[0].ReadOnly {
			t.Fatal("the Pipeline listing lost the read-only flag")
		}
	}
}

// The flag is only worth anything if it reaches the Worker, so this follows it
// all the way from the Pipeline row to the claim a Worker executes.
func TestReadOnlyReachesTheClaimedStage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pipeline := readOnlyStagePipeline(t, store, "Read only claim")
	worker := eligibleWorkerForAdmission(t, store, workerA)
	response, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: "51000000-0000-4000-8000-000000000001",
		PipelineID: pipeline.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{
		RequestID: "read-only-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if claim.Session.ID != response.WorkIDs[0] {
		t.Fatalf("claimed %q, want the admitted Work %q", claim.Session.ID, response.WorkIDs[0])
	}
	if len(claim.Session.Stages) != 1 || !claim.Session.Stages[0].ReadOnly {
		t.Fatalf("the claimed stage is not read only: %#v", claim.Session.Stages)
	}
}

// Only claude-code can deny tools. A read-only stage on any other runtime is a
// promise Factory cannot keep, so the Run is refused at the freeze rather than
// executed with the flag silently dropped.
func TestReadOnlyStageIsRefusedOnARuntimeThatCannotEnforceIt(t *testing.T) {
	store := newTestStore(t)
	pipeline := readOnlyStagePipeline(t, store, "Read only codex")
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	_, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: "51000000-0000-4000-8000-000000000002",
		Repository: repository.RemoteIdentity,
		Name:       "Read the tree",
		Spec:       "Read and report.",
		Runtime:    protocol.RuntimeCodex,
		Source:     protocol.WorkSourceCockpit,
		PipelineID: pipeline.ID,
	})
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "read_only_runtime_unsupported" ||
		serviceErr.Status != 409 {
		t.Fatalf("admission = %#v, want 409 read_only_runtime_unsupported", err)
	}
}

// A read-only stage has nothing to report under the stage report contract: it
// changed nothing and ran no commands. Leaving the contract on would also
// append prose after a result whose whole value is that it parses.
func TestReadOnlyPromptDropsTheStageReportContract(t *testing.T) {
	const body = "Return JSON and nothing else."
	readOnly := protocol.FormatReadOnlyAgentPrompt("Critique", "github.com/example/scratch",
		"factory/work-1", "main", body)
	writable := protocol.FormatAgentPrompt("Critique", "github.com/example/scratch",
		"factory/work-1", "main", body)
	if strings.Contains(readOnly, protocol.StageReportContract) {
		t.Error("a read-only prompt still asks for a Changes and Verification report")
	}
	if !strings.Contains(writable, protocol.StageReportContract) {
		t.Error("the ordinary prompt lost its stage report contract")
	}
	if !strings.HasSuffix(readOnly, body) {
		t.Errorf("a read-only prompt does not end on the task: %.60q",
			readOnly[max(0, len(readOnly)-60):])
	}
	if !strings.Contains(readOnly, protocol.ReadOnlyAgentPosture) {
		t.Error("a read-only prompt does not say the stage is read only")
	}
	if !strings.Contains(readOnly, protocol.AttributionPolicy) {
		t.Error("a read-only prompt dropped the attribution policy every prompt carries")
	}
}
