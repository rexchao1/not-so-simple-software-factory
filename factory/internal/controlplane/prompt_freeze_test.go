package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

const freezeRepositoryIdentity = "github.com/example/freeze"

const freezeSpec = `## Add a farewell function

### Done when
- farewell('world') returns Goodbye, world!

### Out of scope
- Renaming the existing module.`

// AC-9 and INV-9: every rendered stage prompt carries the whole frozen spec,
// never a summary produced by a previous stage.
func TestEveryStagePromptContainsTheFrozenSpec(t *testing.T) {
	store := newTestStore(t)
	eligibleWorkerFor(t, store, "worker-freeze", "freeze", freezeRepositoryIdentity, admissionRuntime)
	repository := registerTestRepository(t, store, freezeRepositoryIdentity)

	pipeline, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: "Three stages",
		Stages: []protocol.PipelineStage{
			{Position: 0, Name: "Implement", Prompt: "Implement this.\n\n{{ task.prompt }}"},
			{Position: 1, Name: "Test", Prompt: "Test this.\n\n{{ task.prompt }}"},
			{Position: 2, Name: "Review", Prompt: "Review this.\n\n{{ task.prompt }}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	admission, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey:  "22000000-0000-4000-8000-000000000001",
		Repository:  repository.RemoteIdentity,
		Name:        "Add a farewell function",
		Spec:        freezeSpec,
		Runtime:     admissionRuntime,
		Source:      protocol.WorkSourceOrchestrator,
		PreApproved: true,
		PipelineID:  pipeline.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The frozen spec only matters if the Work is actually going to run, so
	// this fixture proves it reached queued rather than stalling blocked.
	if admission.State != protocol.SessionQueued {
		t.Fatalf("state = %q, want queued", admission.State)
	}
	assertQueued(t, store, admission.WorkIDs[0])

	rows, err := store.db.QueryContext(context.Background(), `
		SELECT position, prompt FROM session_stages WHERE session_id = ? ORDER BY position
	`, admission.WorkIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stages := 0
	for rows.Next() {
		var position int
		var prompt string
		if err := rows.Scan(&position, &prompt); err != nil {
			t.Fatal(err)
		}
		stages++
		// Exactly once: the INV-9 guard in renderPipelinePrompt must not
		// append a second copy to a stage whose template already rendered it.
		if got := strings.Count(prompt, freezeSpec); got != 1 {
			t.Errorf("stage %d contains the full frozen spec %d times, want 1:\n%s", position, got, prompt)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if stages != 3 {
		t.Fatalf("stages rendered = %d, want 3", stages)
	}
}

// INV-9 is a property of the system, not of a well-written Pipeline. A stage
// template that never references {{ task.prompt }} must still receive the
// complete frozen spec, or a fresh agent starts that stage with a handoff
// note and no contract. The second stage below is exactly the shape the
// invariant forbids: "review what the previous stage did", with no spec.
func TestStageOmittingTheSpecVariableStillReceivesIt(t *testing.T) {
	store := newTestStore(t)
	eligibleWorkerFor(t, store, "worker-omit", "omit", freezeRepositoryIdentity, admissionRuntime)
	repository := registerTestRepository(t, store, freezeRepositoryIdentity)

	pipeline, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: "Second stage forgets the spec",
		Stages: []protocol.PipelineStage{
			{Position: 0, Name: "Implement", Prompt: "Implement this.\n\n{{ task.prompt }}"},
			{Position: 1, Name: "Review", Prompt: "Review what the previous stage did."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	admission, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey:  "22000000-0000-4000-8000-000000000002",
		Repository:  repository.RemoteIdentity,
		Name:        "Add a farewell function",
		Spec:        freezeSpec,
		Runtime:     admissionRuntime,
		Source:      protocol.WorkSourceOrchestrator,
		PreApproved: true,
		PipelineID:  pipeline.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	prompts := stagePromptsFor(t, store, admission.WorkIDs[0])
	if len(prompts) != 2 {
		t.Fatalf("stages rendered = %d, want 2", len(prompts))
	}
	// The stage that asked for the spec gets it exactly once: the guard must
	// not append a second copy on top of the one the template already
	// rendered.
	if got := strings.Count(prompts[0], freezeSpec); got != 1 {
		t.Errorf("stage 0 contains the frozen spec %d times, want 1:\n%s", got, prompts[0])
	}
	// The stage that forgot it gets it anyway.
	if got := strings.Count(prompts[1], freezeSpec); got != 1 {
		t.Errorf("stage 1 contains the frozen spec %d times, want 1:\n%s", got, prompts[1])
	}
	// The stage's own instruction survives; the spec is added, not substituted.
	if !strings.Contains(prompts[1], "Review what the previous stage did.") {
		t.Errorf("stage 1 lost its own instruction:\n%s", prompts[1])
	}
}

func stagePromptsFor(t *testing.T, store *Store, sessionID string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `
		SELECT prompt FROM session_stages WHERE session_id = ? ORDER BY position
	`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	prompts := []string{}
	for rows.Next() {
		var prompt string
		if err := rows.Scan(&prompt); err != nil {
			t.Fatal(err)
		}
		prompts = append(prompts, prompt)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return prompts
}
