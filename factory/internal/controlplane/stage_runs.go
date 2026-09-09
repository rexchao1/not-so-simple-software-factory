package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/owainlewis/factory/internal/protocol"
)

type rowScanner interface {
	Scan(...any) error
}

// stageCostColumns is the six nullable cost columns of a stage row as read
// back from the database: the most recent run of that stage, or NULL in every
// column when it was never measured.
const stageCostColumns = `cost_usd, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, models`

// stageCost receives a scan of stageCostColumns.
type stageCost struct {
	costUSD                                             sql.NullFloat64
	inputTokens, cacheCreation, cacheRead, outputTokens sql.NullInt64
	models                                              sql.NullString
}

// targets returns the scan destinations in stageCostColumns order.
func (c *stageCost) targets() []any {
	return []any{&c.costUSD, &c.inputTokens, &c.cacheCreation, &c.cacheRead, &c.outputTokens, &c.models}
}

// apply sets the three cost fields of a stage from the scanned columns, each
// left nil when its columns are NULL.
func (c stageCost) apply(stage *protocol.StageRun) error {
	if c.costUSD.Valid {
		stage.CostUSD = &c.costUSD.Float64
	}
	// A completion with usage writes all four counts together, so one valid
	// count means the usage was measured.
	if c.inputTokens.Valid || c.cacheCreation.Valid || c.cacheRead.Valid || c.outputTokens.Valid {
		stage.Usage = &protocol.Usage{
			InputTokens:              c.inputTokens.Int64,
			CacheCreationInputTokens: c.cacheCreation.Int64,
			CacheReadInputTokens:     c.cacheRead.Int64,
			OutputTokens:             c.outputTokens.Int64,
		}
	}
	if c.models.Valid {
		if err := json.Unmarshal([]byte(c.models.String), &stage.Models); err != nil {
			return fmt.Errorf("decode stage %d models: %w", stage.Position, err)
		}
	}
	return nil
}

// bound returns the scanned columns in the shape attemptCostColumns builds
// from a request, so a replayed completion can be compared with what the
// stage row already holds.
func (c stageCost) bound() attemptCost {
	var columns attemptCost
	if c.costUSD.Valid {
		columns.costUSD = c.costUSD.Float64
	}
	if c.inputTokens.Valid || c.cacheCreation.Valid || c.cacheRead.Valid || c.outputTokens.Valid {
		columns.inputTokens = c.inputTokens.Int64
		columns.cacheCreationInputTokens = c.cacheCreation.Int64
		columns.cacheReadInputTokens = c.cacheRead.Int64
		columns.outputTokens = c.outputTokens.Int64
	}
	if c.models.Valid {
		columns.models = c.models.String
	}
	return columns
}

func scanStageRun(row rowScanner) (protocol.StageRun, error) {
	var stage protocol.StageRun
	var result, failure string
	var started, completed sql.NullInt64
	var cost stageCost
	targets := []any{&stage.Position, &stage.Name, &stage.Kind, &stage.Prompt, &stage.Command,
		&stage.Model, &stage.Effort, &stage.ReadOnly, &stage.State, &result, &failure, &started, &completed}
	err := row.Scan(append(targets, cost.targets()...)...)
	if err != nil {
		return stage, err
	}
	stage.Result, stage.Error = result, failure
	if started.Valid {
		value := fromMillis(started.Int64)
		stage.StartedAt = &value
	}
	if completed.Valid {
		value := fromMillis(completed.Int64)
		stage.CompletedAt = &value
	}
	if err := cost.apply(&stage); err != nil {
		return stage, err
	}
	return stage, nil
}

func (s *Store) StartStage(ctx context.Context, attemptID string, position int, input protocol.StartStageRequest) (protocol.StageRun, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.StageRun{}, err
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return protocol.StageRun{}, err
	}
	if lease.attemptState != "running" || lease.executionState != "running" {
		return protocol.StageRun{}, conflict("attempt_not_running", "Pipeline stages require a running Attempt")
	}
	if lease.cancel {
		return protocol.StageRun{}, conflict("cancellation_requested", "the Attempt is being cancelled")
	}
	var sessionID, state string
	err = tx.QueryRowContext(ctx, `
		SELECT execution.session_id, stage.state
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN session_stages stage ON stage.session_id = execution.session_id AND stage.position = ?
		WHERE attempt.id = ?
	`, position, attemptID).Scan(&sessionID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.StageRun{}, ErrNotFound
	}
	if err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if state != string(protocol.StagePending) && state != string(protocol.StageRunning) {
		return protocol.StageRun{}, conflict("stage_not_pending", "the Pipeline stage cannot be started from its current state")
	}
	var incomplete int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_stages WHERE session_id = ? AND position < ? AND state != 'succeeded'`, sessionID, position).Scan(&incomplete); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if incomplete != 0 {
		return protocol.StageRun{}, conflict("stage_predecessor_incomplete", "the previous Pipeline stage has not succeeded")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = 'running', started_at = COALESCE(started_at, ?)
		WHERE session_id = ? AND position = ?
	`, now, sessionID, position); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE attempts SET supervisor_pid = ?, process_identity = ?, process_group_id = ? WHERE id = ?
	`, input.SupervisorPID, nullString(input.ProcessIdentity), input.ProcessGroupID, attemptID); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	return s.stageRun(ctx, sessionID, position)
}

func (s *Store) CompleteStage(ctx context.Context, attemptID string, position int, input protocol.CompleteStageRequest) (protocol.StageRun, error) {
	if input.State != protocol.StageSucceeded && input.State != protocol.StageFailed && input.State != protocol.StageCancelled {
		return protocol.StageRun{}, invalid("invalid_stage_state", "state must be succeeded, failed, or cancelled")
	}
	if len([]byte(input.Result)) > protocol.MaxResultBytes || len([]byte(input.Error)) > protocol.MaxErrorBytes {
		return protocol.StageRun{}, invalid("stage_result_too_large", "stage result or error exceeds its storage limit")
	}
	if !protocol.SupportedReviewVerdict(input.ReviewVerdict) {
		return protocol.StageRun{}, invalid(
			"invalid_review_verdict", "review_verdict must be approve, request-changes, or blocked")
	}
	if input.ReviewVerdict != protocol.ReviewVerdictNone && position == 0 {
		// design.md:249-254 justifies a separate reviewing stage precisely
		// because the reviewer must not have written the code. Position 0 is
		// the implementer, and a single-stage Pipeline has no one else, so a
		// verdict there is self-approval however it is labelled.
		return protocol.StageRun{}, invalid(
			"invalid_review_verdict",
			"the first Pipeline stage implements the work and cannot record a review verdict on it")
	}
	cost, err := attemptCostColumns(input.CostUSD, input.Usage, input.Models)
	if err != nil {
		return protocol.StageRun{}, err
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.StageRun{}, err
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return protocol.StageRun{}, err
	}
	if lease.attemptState != "running" || lease.executionState != "running" {
		return protocol.StageRun{}, conflict("attempt_not_running", "Pipeline stages require a running Attempt")
	}
	var sessionID, state, result, failure, verdict string
	var stored stageCost
	targets := []any{&sessionID, &state, &result, &failure, &verdict}
	err = tx.QueryRowContext(ctx, `
		SELECT execution.session_id, stage.state, stage.result, stage.error, stage.review_verdict,
		       stage.cost_usd, stage.input_tokens, stage.cache_creation_input_tokens,
		       stage.cache_read_input_tokens, stage.output_tokens, stage.models
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN session_stages stage ON stage.session_id = execution.session_id AND stage.position = ?
		WHERE attempt.id = ?
	`, position, attemptID).Scan(append(targets, stored.targets()...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.StageRun{}, ErrNotFound
	}
	if err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if state != string(protocol.StageRunning) {
		// The cost columns take part in the replay comparison: every field of
		// attemptCost is nil or a comparable scalar, so struct equality says
		// whether the stored cost is the one this completion carries.
		if state == string(input.State) && result == input.Result && failure == input.Error &&
			verdict == string(input.ReviewVerdict) && stored.bound() == cost {
			if err := tx.Commit(); err != nil {
				return protocol.StageRun{}, unavailable(err)
			}
			return s.stageRun(ctx, sessionID, position)
		}
		return protocol.StageRun{}, conflict("stage_not_running", "the Pipeline stage cannot complete from its current state")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = ?, result = ?, error = ?, review_verdict = ?, completed_at = ?,
		       cost_usd = ?, input_tokens = ?, cache_creation_input_tokens = ?,
		       cache_read_input_tokens = ?, output_tokens = ?, models = ?
		WHERE session_id = ? AND position = ? AND state = 'running'
	`, input.State, input.Result, input.Error, input.ReviewVerdict, now,
		cost.costUSD, cost.inputTokens, cost.cacheCreationInputTokens,
		cost.cacheReadInputTokens, cost.outputTokens, cost.models, sessionID, position); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if input.State != protocol.StageSucceeded {
		if _, err := tx.ExecContext(ctx, `UPDATE session_stages SET state = 'cancelled', completed_at = ? WHERE session_id = ? AND position > ? AND state = 'pending'`, now, sessionID, position); err != nil {
			return protocol.StageRun{}, unavailable(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	return s.stageRun(ctx, sessionID, position)
}

func (s *Store) stageRun(ctx context.Context, sessionID string, position int) (protocol.StageRun, error) {
	var stage protocol.StageRun
	var started, completed sql.NullInt64
	var cost stageCost
	targets := []any{&stage.Position, &stage.Name, &stage.Kind, &stage.Model, &stage.Effort,
		&stage.State, &stage.ReviewVerdict, &started, &completed}
	err := s.db.QueryRowContext(ctx, `
		SELECT position, name, kind, model, effort, state, review_verdict, started_at, completed_at, `+stageCostColumns+`
		FROM session_stages WHERE session_id = ? AND position = ?
	`, sessionID, position).Scan(append(targets, cost.targets()...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return stage, ErrNotFound
	}
	if err != nil {
		return stage, unavailable(err)
	}
	if started.Valid {
		value := fromMillis(started.Int64)
		stage.StartedAt = &value
	}
	if completed.Valid {
		value := fromMillis(completed.Int64)
		stage.CompletedAt = &value
	}
	if err := cost.apply(&stage); err != nil {
		return stage, unavailable(err)
	}
	return stage, nil
}

// ReviewVerdict returns the verdict of the highest-position stage that recorded
// one, and the empty verdict when no stage did. INV-8 fails closed on the empty
// verdict: no reviewing stage means no merge.
//
// Highest position wins so that a later stage can block work an earlier one
// approved. The reverse would let a pipeline approve, then find a problem, and
// merge anyway.
func (s *Store) ReviewVerdict(ctx context.Context, workID string) (protocol.ReviewVerdict, error) {
	var verdict string
	err := s.db.QueryRowContext(ctx, `
		SELECT review_verdict FROM session_stages
		WHERE session_id = ? AND review_verdict != ''
		ORDER BY position DESC LIMIT 1
	`, workID).Scan(&verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.ReviewVerdictNone, nil
	}
	if err != nil {
		return protocol.ReviewVerdictNone, unavailable(err)
	}
	return protocol.ReviewVerdict(verdict), nil
}
