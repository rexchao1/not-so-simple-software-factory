package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

// factory_settings holds exactly one row, guarded by a CHECK on its primary
// key. It is the global floor of the stage execution precedence chain.

// Pause refuses two distinct things, and the wording has to say which, because
// an operator reading a rejected schedule occurrence and an operator reading a
// stalled Worker are diagnosing different situations. Both share the
// factory_paused code so a client can discriminate without parsing prose.
const (
	pauseAdmissionMessage = "Factory is paused; new Work cannot be admitted"
	pauseDispatchMessage  = "Factory is paused; queued Work will not be dispatched"
)

// pauseGate refuses a mutation that would admit new Work or dispatch existing
// Work while Factory is paused.
//
// It reads inside the caller's transaction deliberately. Reading on the
// Store's own connection before opening the transaction, as two admission
// paths used to, leaves a window in which a pause committed between the read
// and the insert still admits Work. Every caller must therefore hold the
// transaction it is about to write in.
//
// Callers must also place this AFTER their request-key replay lookup. An
// already-admitted key has to keep returning its original Run while paused, or
// a client retry turns a completed admission into a hard failure and the CLI's
// durable admission journal is left without an authoritative result (INV-13,
// INV-14).
func pauseGate(ctx context.Context, tx *sql.Tx, message string) error {
	var paused int
	if err := tx.QueryRowContext(ctx, `SELECT paused FROM factory_settings WHERE id = 1`).Scan(&paused); err != nil {
		return unavailable(err)
	}
	if paused != 0 {
		return conflict("factory_paused", message)
	}
	return nil
}

func (s *Store) FactoryPause(ctx context.Context) (protocol.FactoryPause, error) {
	var value protocol.FactoryPause
	var paused int
	var at sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT paused, paused_at FROM factory_settings WHERE id = 1`).Scan(&paused, &at); err != nil {
		return value, unavailable(err)
	}
	value.Paused = paused != 0
	if at.Valid {
		timestamp := time.UnixMilli(at.Int64).UTC()
		value.PausedAt = &timestamp
	}
	return value, nil
}

func (s *Store) SetFactoryPause(ctx context.Context, input protocol.FactoryPause) (protocol.FactoryPause, error) {
	if !input.Paused {
		input.PausedAt = nil
	}
	now := s.now().UTC()
	var at any
	if input.Paused {
		at = now.UnixMilli()
		input.PausedAt = &now
	}
	result, err := s.db.ExecContext(ctx, `UPDATE factory_settings SET paused = ?, paused_at = ?, updated_at = ? WHERE id = 1`, boolToInt(input.Paused), at, now.UnixMilli())
	if err != nil {
		return protocol.FactoryPause{}, unavailable(err)
	}
	// The singleton row is created by migration and guarded by a CHECK on its
	// primary key, so a zero-row update means the row is gone. Reporting
	// success would tell an operator that Factory is paused when it is not.
	affected, err := result.RowsAffected()
	if err != nil {
		return protocol.FactoryPause{}, unavailable(err)
	}
	if affected == 0 {
		return protocol.FactoryPause{}, unavailable(errors.New("factory_settings has no singleton row to update"))
	}
	if !input.Paused {
		// Worker claims materialise blocked Work themselves, so the fleet
		// recovers on its own poll. This wakes the control plane's own loops,
		// whose held schedule occurrences would otherwise wait out a retry
		// delay after the operator has already resumed.
		s.signalResumed()
	}
	return input, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) StageDefaults(ctx context.Context) (protocol.StageDefaults, error) {
	var defaults protocol.StageDefaults
	err := s.db.QueryRowContext(ctx,
		`SELECT default_model, default_effort FROM factory_settings WHERE id = 1`,
	).Scan(&defaults.Model, &defaults.Effort)
	if err != nil {
		return protocol.StageDefaults{}, unavailable(err)
	}
	return defaults, nil
}

// stageDefaultsTx reads the same row inside an open transaction. Admission
// resolves the chain while it holds the transaction that writes the Run, so it
// cannot use the Store method without reading outside its own snapshot.
func stageDefaultsTx(ctx context.Context, tx *sql.Tx) (protocol.StageDefaults, error) {
	var defaults protocol.StageDefaults
	err := tx.QueryRowContext(ctx,
		`SELECT default_model, default_effort FROM factory_settings WHERE id = 1`,
	).Scan(&defaults.Model, &defaults.Effort)
	if err != nil {
		return protocol.StageDefaults{}, unavailable(err)
	}
	return defaults, nil
}

func (s *Store) SaveStageDefaults(
	ctx context.Context, input protocol.StageDefaults,
) (protocol.StageDefaults, error) {
	value := protocol.StageDefaults{
		Model:  strings.TrimSpace(input.Model),
		Effort: strings.TrimSpace(input.Effort),
	}
	// Empty clears the default, which is a real operation: it returns the
	// factory to passing no argument at all.
	if value.Model != "" && !protocol.SupportedModel(protocol.RuntimeClaudeCode, value.Model) {
		return protocol.StageDefaults{}, invalid(
			"invalid_stage_default_model", "default model must be one of: opus, sonnet, haiku, fable")
	}
	if value.Effort != "" && !protocol.SupportedEffort(value.Effort) {
		return protocol.StageDefaults{}, invalid(
			"invalid_stage_default_effort", "default effort must be one of: low, medium, high, xhigh, max")
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE factory_settings SET default_model = ?, default_effort = ?, updated_at = ? WHERE id = 1`,
		value.Model, value.Effort, s.now().UnixMilli()); err != nil {
		return protocol.StageDefaults{}, unavailable(err)
	}
	return value, nil
}
