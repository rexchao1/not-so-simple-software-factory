package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// critiquePipeline reads the built-in back through the ordinary Pipeline
// loader, so these tests see exactly what an API caller and an admission see.
func critiquePipeline(t *testing.T, store *Store) protocol.Pipeline {
	t.Helper()
	pipeline, err := store.Pipeline(context.Background(), protocol.CritiquePipelineID)
	if err != nil {
		t.Fatalf("read the built-in Critique Pipeline: %v", err)
	}
	return pipeline
}

// The stage settings are the contract the light-factory skill was written
// against, so each one is asserted rather than the shape as a whole.
func TestCritiquePipelineIsInstalledAtStartup(t *testing.T) {
	pipeline := critiquePipeline(t, newTestStore(t))
	if pipeline.Name != "Critique" {
		t.Errorf("name = %q, want Critique", pipeline.Name)
	}
	if len(pipeline.Stages) != 1 {
		t.Fatalf("stages = %d, want exactly one; a critique has no delivery stage", len(pipeline.Stages))
	}
	stage := pipeline.Stages[0]
	if protocol.StageKind(stage.Kind) != protocol.StageKindAgent {
		t.Errorf("stage kind = %q, want agent", stage.Kind)
	}
	if !stage.ReadOnly {
		t.Error("the critique stage is not read only, so nothing stops the critic writing")
	}
	if stage.Model != "opus" || stage.Effort != protocol.EffortXHigh {
		t.Errorf("model and effort = %q %q, want opus xhigh from the critic row of models.tsv",
			stage.Model, stage.Effort)
	}
	if stage.Command != "" {
		t.Errorf("an agent stage carries no command, got %q", stage.Command)
	}
	for _, stage := range pipeline.Stages {
		if protocol.IsDeliveryStage(stage.Kind) {
			t.Error("a critique commits nothing, so it must not have a delivery stage")
		}
	}
}

// The prompt is the skill body verbatim, then a blank line, then the PRD. The
// body is checked at both ends because a copy that lost its opening heading or
// gained its frontmatter back is still a plausible-looking prompt.
func TestCritiqueStagePromptIsTheSkillBodyThenTheTask(t *testing.T) {
	stage := critiquePipeline(t, newTestStore(t)).Stages[0]
	if strings.HasPrefix(strings.TrimSpace(critiqueSkillBody), "---") {
		t.Fatal("the embedded skill body still carries its frontmatter")
	}
	if !strings.HasPrefix(stage.Prompt, "# checkpoint-critic\n") {
		t.Errorf("prompt does not open with the skill body: %.40q", stage.Prompt)
	}
	if !strings.Contains(stage.Prompt, "Return JSON and nothing else.") {
		t.Error("prompt lost the instruction that makes the result machine readable")
	}
	if !strings.HasSuffix(stage.Prompt, "\n\n{{ task.prompt }}") {
		t.Errorf("prompt does not end with a blank line and the task variable: %.60q",
			stage.Prompt[max(0, len(stage.Prompt)-60):])
	}
	body := strings.TrimSuffix(stage.Prompt, "\n\n{{ task.prompt }}")
	if body != strings.TrimSpace(critiqueSkillBody) {
		t.Error("the prompt is not the embedded skill body verbatim")
	}
}

// Deleting it would leave the light factory submitting to an id that no longer
// exists, which is the same failure Single agent's guard prevents.
func TestCritiquePipelineCannotBeDeleted(t *testing.T) {
	store := newTestStore(t)
	err := store.DeletePipeline(context.Background(), protocol.CritiquePipelineID)
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "pipeline_delete_not_allowed" {
		t.Fatalf("delete = %#v, want pipeline_delete_not_allowed", err)
	}
	if _, err := store.Pipeline(context.Background(), protocol.CritiquePipelineID); err != nil {
		t.Fatalf("the Pipeline did not survive the refused delete: %v", err)
	}
}

// The prompt is a copy of a skill that keeps being edited, so the built-in has
// to be repaired and not merely installed once. This edits the stored stage
// the way a stale install or a hand edit would, reopens the store, and expects
// the definition back.
func TestCritiquePipelineIsRepairedOnRestart(t *testing.T) {
	path := t.TempDir() + "/controlplane.sqlite3"
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	before := critiquePipeline(t, store)
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE pipeline_stages SET prompt = 'stale', model = 'haiku', read_only = 0
		WHERE pipeline_id = ?
	`, protocol.CritiquePipelineID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	after := critiquePipeline(t, reopened)
	if len(after.Stages) != 1 || after.Stages[0] != before.Stages[0] {
		t.Fatalf("the tampered stage was not repaired: %#v", after.Stages)
	}
	if after.Generation <= before.Generation {
		t.Errorf("generation = %d, want above %d; a repair is a new revision",
			after.Generation, before.Generation)
	}
}

// A restart that changes nothing must not look like an edit, or every Pipeline
// list would reorder and every generation would climb on each server start.
func TestCritiquePipelineRepairIsQuietWhenNothingChanged(t *testing.T) {
	path := t.TempDir() + "/controlplane.sqlite3"
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	before := critiquePipeline(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	after := critiquePipeline(t, reopened)
	if after.Generation != before.Generation || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("an unchanged restart moved the Pipeline: %d %v then %d %v",
			before.Generation, before.UpdatedAt, after.Generation, after.UpdatedAt)
	}
}
