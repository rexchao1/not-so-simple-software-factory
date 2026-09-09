package controlplane

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	"github.com/owainlewis/factory/internal/protocol"
)

// The built-in Critique Pipeline is the light factory's one non-interactive
// model call. A checkpoint PRD is admitted as Work on it, one read-only agent
// reads the PRD and the repository it describes, and the findings come back as
// the Work's result.
//
// It is a built-in for the same reason Single agent is: the light-factory
// skill submits to it by a stable id, and a pipeline an operator has to create
// by hand before the skill works is a pipeline that will be missing on a fresh
// install and mis-typed on every other one.
//
// Three properties are the whole design.
//
//   - The stage is read only. Decision 2 of the light and dark PRD is that the
//     critic's read-only guarantee is enforced by the Worker and not by a
//     prompt, and the flag is what carries that into the runtime arguments.
//   - There is no delivery stage. A critique produces no commit, so pushing a
//     branch and opening a pull request would deliver an empty diff.
//   - The outcome contract is process_exit, which admission stamps on Work
//     submitted to this Pipeline. agent_update would put a Factory reporting
//     contract in front of the agent and require it to call back over a
//     socket, and the result Factory wants here is simply what the critic
//     printed.

// critiqueSkillBody is the checkpoint-critic skill body, verbatim, everything
// after that skill's frontmatter.
//
// It is a copy rather than a read of the installed skill, and deliberately so.
// The Pipeline's prompt is frozen onto every Run admitted against it, so the
// server has to be able to state it with no filesystem outside its own binary,
// on a machine where the skill library may not be installed at all. Keeping it
// as its own file rather than a Go string literal is what lets the copy be
// diffed against the skill it came from.
//
//go:embed critique_prompt.md
var critiqueSkillBody string

const (
	critiquePipelineName = "Critique"
	critiqueStageName    = "Critique"
	// The critic's model and effort are the `critic` row of the light factory's
	// models.tsv, resolved to the ids Factory validates: that row names
	// claude-opus-5 at xhigh, and Factory's curated aliases carry the tier
	// rather than the release, so the alias is what is stored.
	critiqueStageModel  = "opus"
	critiqueStageEffort = protocol.EffortXHigh
)

// critiqueStagePrompt is the skill body followed by the PRD under critique.
// task.prompt is last because it is the untrusted half: the instructions the
// agent follows are stated before the document it reads.
func critiqueStagePrompt() string {
	return critiqueSkillBody + "\n{{ task.prompt }}"
}

// critiquePipelineDefinition is the one statement of what the built-in is.
// Both the install and the repair below read it, so the two cannot disagree
// about what a Critique Pipeline is supposed to look like.
func critiquePipelineDefinition() protocol.SavePipelineRequest {
	return protocol.SavePipelineRequest{
		Name: critiquePipelineName,
		Stages: []protocol.PipelineStage{{
			Position: 0,
			Name:     critiqueStageName,
			Kind:     protocol.StageKindAgent,
			Prompt:   critiqueStagePrompt(),
			Model:    critiqueStageModel,
			Effort:   critiqueStageEffort,
			ReadOnly: true,
		}},
	}
}

// ensureCritiquePipeline installs the built-in Critique Pipeline, or repairs
// it, on every start.
//
// Repairing rather than installing once is the point. The stage prompt is a
// copy of a skill that keeps being edited, and its model comes from a table
// that changes when a new model tier appears, so a Pipeline written by a
// migration would be correct exactly until the first of those moved. Runs
// already admitted are unaffected: they carry a frozen Pipeline snapshot, so a
// repair changes what the next submission runs and never what a running one
// does.
//
// The rows are written directly rather than through CreatePipeline and
// UpdatePipeline, because those two allocate an id and require an expected
// generation, and this Pipeline's id is fixed and its generation belongs to
// the server rather than to an operator's optimistic concurrency check. The
// definition is still put through normalizePipeline first, so the built-in is
// held to the same validation an operator's Pipeline is.
func (s *Store) ensureCritiquePipeline(ctx context.Context) error {
	fail := func(err error) error {
		return fmt.Errorf("install built-in Critique Pipeline: %w", err)
	}
	name, stages, err := normalizePipeline(critiquePipelineDefinition())
	if err != nil {
		return fail(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	var storedName string
	err = tx.QueryRowContext(ctx, `SELECT name FROM pipelines WHERE id = ?`,
		protocol.CritiquePipelineID).Scan(&storedName)
	switch {
	case err == nil:
		// The generation moves only when the definition actually changed, so
		// a restart that installs the same text does not look like an edit.
		changed, changedErr := critiqueStagesDiffer(ctx, tx, stages)
		if changedErr != nil {
			return fail(changedErr)
		}
		if !changed && storedName == name {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE pipelines SET name = ?, name_key = ?, generation = generation + 1, updated_at = ?
			WHERE id = ?
		`, name, normalizeTitleKey(name), now, protocol.CritiquePipelineID); err != nil {
			return fail(err)
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO pipelines(id, name, name_key, generation, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)
		`, protocol.CritiquePipelineID, name, normalizeTitleKey(name), now, now); err != nil {
			return fail(err)
		}
	default:
		return fail(err)
	}
	if err := replacePipelineStages(ctx, tx, protocol.CritiquePipelineID, stages); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return nil
}

// critiqueStagesDiffer compares the stored stages against the definition field
// by field. Comparing rather than rewriting unconditionally keeps updated_at,
// which orders the Pipelines list, from moving on every restart.
func critiqueStagesDiffer(
	ctx context.Context, tx *sql.Tx, stages []protocol.PipelineStage,
) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT position, name, kind, prompt, command, model, effort, read_only
		FROM pipeline_stages WHERE pipeline_id = ? ORDER BY position
	`, protocol.CritiquePipelineID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	stored := []protocol.PipelineStage{}
	for rows.Next() {
		var stage protocol.PipelineStage
		if err := rows.Scan(&stage.Position, &stage.Name, &stage.Kind, &stage.Prompt,
			&stage.Command, &stage.Model, &stage.Effort, &stage.ReadOnly); err != nil {
			return false, err
		}
		stored = append(stored, stage)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(stored) != len(stages) {
		return true, nil
	}
	for index, stage := range stages {
		if stored[index] != stage {
			return true, nil
		}
	}
	return false, nil
}
