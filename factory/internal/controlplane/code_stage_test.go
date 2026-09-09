package controlplane

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func createPipelineForTest(t *testing.T, store *Store, name string, stages []protocol.PipelineStage) (protocol.Pipeline, error) {
	t.Helper()
	return store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{Name: name, Stages: stages})
}

// A code stage is defined by carrying a command and no prompt. Both halves are
// enforced, because a stage that carries both would leave it ambiguous whether
// a model runs, and INV-7 has to be decidable from the stage alone.
func TestCodeStageValidationRequiresACommandAndNoPrompt(t *testing.T) {
	store := newTestStore(t)
	cases := []struct {
		name  string
		stage protocol.PipelineStage
		code  string
	}{
		{
			name:  "code stage with no command",
			stage: protocol.PipelineStage{Name: "Gate", Kind: protocol.StageKindCode},
			code:  "invalid_pipeline_stage_command",
		},
		{
			name: "code stage carrying a prompt",
			stage: protocol.PipelineStage{
				Name: "Gate", Kind: protocol.StageKindCode, Command: "npm test", Prompt: "Please test.",
			},
			code: "invalid_pipeline_stage_prompt",
		},
		{
			name: "agent stage carrying a command",
			stage: protocol.PipelineStage{
				Name: "Build", Prompt: "Make the change.", Command: "npm test",
			},
			code: "invalid_pipeline_stage_command",
		},
		{
			name:  "unknown kind",
			stage: protocol.PipelineStage{Name: "Gate", Kind: "shell", Command: "npm test"},
			code:  "invalid_pipeline_stage_kind",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := createPipelineForTest(t, store, testCase.name, []protocol.PipelineStage{testCase.stage})
			if !serviceErrorCode(err, testCase.code) {
				t.Fatalf("CreatePipeline error = %v, want %s", err, testCase.code)
			}
		})
	}
}

// A code stage survives the round trip with its command intact and no prompt,
// and an agent stage is unchanged by the migration that added the columns.
func TestPipelineRoundTripsBothStageKinds(t *testing.T) {
	store := newTestStore(t)
	created, err := createPipelineForTest(t, store, "Build then gate", []protocol.PipelineStage{
		{Name: "Build", Prompt: "Make the change: {{ task.prompt }}"},
		{Name: "Tests", Kind: protocol.StageKindCode, Command: "npm test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Pipeline(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Stages) != 2 {
		t.Fatalf("stages = %#v, want 2", read.Stages)
	}
	if read.Stages[0].Kind != protocol.StageKindAgent || read.Stages[0].Command != "" ||
		read.Stages[0].Prompt != "Make the change: {{ task.prompt }}" {
		t.Fatalf("agent stage = %#v", read.Stages[0])
	}
	if read.Stages[1].Kind != protocol.StageKindCode || read.Stages[1].Command != "npm test" ||
		read.Stages[1].Prompt != "" {
		t.Fatalf("code stage = %#v", read.Stages[1])
	}
}

// The default Single agent pipeline is the one every existing Task uses. The
// migration must leave it an agent stage with its prompt untouched.
func TestFastPipelineUsesOneModelAndFactoryDelivery(t *testing.T) {
	store := newTestStore(t)
	pipeline, err := store.Pipeline(t.Context(), protocol.FastPipelineID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pipeline.Stages) != 2 || pipeline.Stages[0].Kind != protocol.StageKindAgent ||
		pipeline.Stages[0].Model != "sonnet" || pipeline.Stages[0].Effort != "medium" ||
		pipeline.Stages[1].Kind != protocol.StageKindDelivery {
		t.Fatalf("fast pipeline = %#v", pipeline.Stages)
	}
	if err := store.DeletePipeline(t.Context(), protocol.FastPipelineID); !serviceErrorCode(err, "pipeline_delete_not_allowed") {
		t.Fatalf("built-in Fast pipeline deletion error = %v", err)
	}
}

func TestDefaultPipelineSurvivesTheStageKindMigration(t *testing.T) {
	store := newTestStore(t)
	pipeline, err := store.Pipeline(context.Background(), protocol.DefaultPipelineID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pipeline.Stages) != 1 || pipeline.Stages[0].Name != "Do the task" ||
		pipeline.Stages[0].Prompt != "{{ task.prompt }}" ||
		protocol.StageKind(pipeline.Stages[0].Kind) != protocol.StageKindAgent ||
		pipeline.Stages[0].Command != "" {
		t.Fatalf("default pipeline stages = %#v", pipeline.Stages)
	}
}

// A code stage never reaches renderPipelinePrompt, so a frozen code stage
// carries the command and an empty prompt. This is where INV-7 starts: a stage
// with no prompt has nothing to send to a model.
func TestFrozenCodeStageCarriesNoPrompt(t *testing.T) {
	stages, err := resolveSessionStages(
		protocol.TaskSnapshot{
			ID: "task", Name: "Build then gate", OutcomeContract: protocol.OutcomeProcessExit,
			Pipeline: protocol.PipelineSnapshot{Stages: []protocol.PipelineStage{
				{Position: 0, Name: "Build", Kind: protocol.StageKindAgent, Prompt: "Do {{ task.prompt }}"},
				{Position: 1, Name: "Tests", Kind: protocol.StageKindCode, Command: "npm test"},
			}},
		},
		"the frozen spec", "run", protocol.WorkTarget{RepositoryIdentity: "github.com/example/scratch"},
		protocol.StageDefaults{Model: "sonnet", Effort: "medium"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 {
		t.Fatalf("stages = %#v, want 2", stages)
	}
	if stages[0].Prompt != "Do the frozen spec" || stages[0].Command != "" ||
		stages[0].Model != "sonnet" || stages[0].Effort != "medium" {
		t.Fatalf("agent stage = %#v, want the rendered spec and resolved execution", stages[0])
	}
	if stages[1].Prompt != "" || stages[1].Command != "npm test" ||
		stages[1].Kind != protocol.StageKindCode {
		t.Fatalf("code stage = %#v, want a command and no prompt", stages[1])
	}
}

// Under agent_update the Worker attaches the report channel to the final stage.
// A pipeline that ends in a code stage has nowhere to report from, so admission
// rejects it rather than letting the run fail later with a misleading
// "Agent exited without reporting an outcome".
func TestAgentUpdateRejectsAPipelineEndingInACodeStage(t *testing.T) {
	stages := []protocol.PipelineStage{
		{Position: 0, Name: "Build", Kind: protocol.StageKindAgent, Prompt: "Make the change."},
		{Position: 1, Name: "Tests", Kind: protocol.StageKindCode, Command: "npm test"},
	}
	if err := checkFinalStageReports(protocol.OutcomeAgentUpdate, stages); !serviceErrorCode(err, "final_stage_cannot_report") {
		t.Fatalf("checkFinalStageReports error = %v, want final_stage_cannot_report", err)
	}
	if err := checkFinalStageReports(protocol.OutcomeProcessExit, stages); err != nil {
		t.Fatalf("process_exit must still allow a trailing code stage: %v", err)
	}
	reporting := append(append([]protocol.PipelineStage{}, stages...),
		protocol.PipelineStage{Position: 2, Name: "Report", Kind: protocol.StageKindAgent, Prompt: "Report the outcome."})
	if err := checkFinalStageReports(protocol.OutcomeAgentUpdate, reporting); err != nil {
		t.Fatalf("a trailing agent stage must be accepted: %v", err)
	}
	delivery := append(append([]protocol.PipelineStage{}, stages...),
		protocol.PipelineStage{Position: 2, Name: "Deliver", Kind: protocol.StageKindDelivery})
	if err := checkFinalStageReports(protocol.OutcomeAgentUpdate, delivery); err != nil {
		t.Fatalf("a trailing Factory delivery stage must be accepted: %v", err)
	}
}

// WorkerDispatched replaced eleven comparisons against BackendPersistent. The
// answers for the two pre-existing backends must be exactly what those
// comparisons gave, or the refactor changed behavior instead of naming it.
func TestWorkerDispatchedPreservesExistingBackendAnswers(t *testing.T) {
	if !protocol.WorkerDispatched(protocol.BackendPersistent) {
		t.Fatal("persistent must stay Worker dispatched")
	}
	if protocol.WorkerDispatched(protocol.BackendFakeCloudRun) {
		t.Fatal("fake_cloud_run must stay control plane synthesized")
	}
	if !protocol.WorkerDispatched(protocol.BackendDocker) {
		t.Fatal("docker runs on a Worker against a real worktree")
	}
}
