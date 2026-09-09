package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

// overviewRecentCostDays is the trailing window the Overview reports alongside
// the lifetime total, so the panel answers "what is this costing me lately" as
// well as "what has it cost me".
//
// Seven days rather than one: a day-scoped figure resets before an operator
// has necessarily looked at it, so a morning check can show almost nothing
// after an expensive night.
const overviewRecentCostDays = 7

// overviewCost summarises what Factory has spent, and how much of that spend
// it cannot see.
//
// Every total is nil rather than zero when nothing reported, because only
// Claude Code reports cost today. A fleet running Codex or Pi has real spend
// these figures cannot observe, and a total that does not say so reads as
// complete when it is not.
func (s *Store) overviewCost(ctx context.Context, now time.Time) (protocol.OverviewCost, error) {
	summary := protocol.OverviewCost{RecentDays: overviewRecentCostDays}

	// Aggregated in SQL rather than by reading every row: this runs on the
	// Overview poll, and a lifetime scan that materialised one row per Work
	// item would grow without bound.
	//
	// The LEFT JOINs mean Work that never reached an Attempt still counts as
	// one unmeasured item rather than vanishing from the denominator.
	var total, highest sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		WITH work_cost AS (
			SELECT session.id AS id, SUM(attempt.cost_usd) AS cost
			FROM sessions session
			LEFT JOIN executions execution ON execution.session_id = session.id
			LEFT JOIN attempts attempt ON attempt.execution_id = execution.id
			WHERE session.terminal_at IS NOT NULL
			GROUP BY session.id
		)
		SELECT
			COALESCE(SUM(CASE WHEN cost IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN cost IS NULL THEN 1 ELSE 0 END), 0),
			SUM(cost),
			MAX(cost)
		FROM work_cost
	`).Scan(&summary.MeasuredWork, &summary.UnavailableWork, &total, &highest)
	if err != nil {
		return summary, unavailable(err)
	}
	if total.Valid {
		value := total.Float64
		summary.TotalUSD = &value
	}
	if summary.MeasuredWork > 0 && total.Valid {
		// Averaged over what was measured, never over everything: dividing by
		// the unmeasured items too would understate the real unit cost.
		average := total.Float64 / float64(summary.MeasuredWork)
		summary.AverageUSD = &average
	}
	if highest.Valid {
		value := highest.Float64
		summary.HighestUSD = &value
		if err := s.dearestWork(ctx, &summary); err != nil {
			return summary, err
		}
	}

	// Scoped to terminal Work like the lifetime total above. Counting an
	// in-flight Work item's finished attempts here but not there would let the
	// trailing window exceed the total it is a subset of.
	since := now.AddDate(0, 0, -overviewRecentCostDays).UnixMilli()
	var recent sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(attempt.cost_usd)
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN sessions session ON session.id = execution.session_id
		WHERE attempt.completed_at >= ? AND attempt.completed_at <= ?
		  AND attempt.cost_usd IS NOT NULL AND session.terminal_at IS NOT NULL
	`, since, now.UnixMilli()).Scan(&recent); err != nil {
		return summary, unavailable(err)
	}
	if recent.Valid {
		value := recent.Float64
		summary.RecentUSD = &value
	}

	byModel, err := s.overviewCostByModel(ctx)
	if err != nil {
		return summary, err
	}
	summary.ByModel = byModel
	return summary, nil
}

// dearestWork names the single most expensive Work item, which is the one an
// operator wants to open when a total looks wrong.
func (s *Store) dearestWork(ctx context.Context, summary *protocol.OverviewCost) error {
	var id, submitted, stored string
	var cost float64
	err := s.db.QueryRowContext(ctx, `
		SELECT session.id,
		       COALESCE(json_extract(run.task_snapshot, '$.submitted_name'), ''),
		       COALESCE(json_extract(run.task_snapshot, '$.name'), ''),
		       SUM(attempt.cost_usd) AS cost
		FROM sessions session
		JOIN runs run ON run.id = session.run_id
		JOIN executions execution ON execution.session_id = session.id
		JOIN attempts attempt ON attempt.execution_id = execution.id
		WHERE session.terminal_at IS NOT NULL AND attempt.cost_usd IS NOT NULL
		GROUP BY session.id
		ORDER BY cost DESC, session.id
		LIMIT 1
	`).Scan(&id, &submitted, &stored, &cost)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	// The stored name carries admission's uniquifying suffix, so the submitted
	// title wins wherever there is one.
	summary.HighestWorkID = id
	summary.HighestWorkName = firstNonEmptyString(submitted, stored)
	summary.HighestUSD = &cost
	return nil
}

// overviewCostByModel rolls the per-model breakdown up over every attempt that
// reported one. The column holds one JSON object per attempt, so the sum
// happens in Go: SQLite cannot aggregate across dynamic JSON keys.
func (s *Store) overviewCostByModel(ctx context.Context) ([]protocol.ModelCost, error) {
	// Scoped to terminal Work like every other figure in the summary. Left
	// unscoped, an in-flight Work item's finished attempts would appear here
	// and the by-model rows could sum to more than the total above them.
	rows, err := s.db.QueryContext(ctx, `
		SELECT attempt.models
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN sessions session ON session.id = execution.session_id
		WHERE attempt.models IS NOT NULL AND session.terminal_at IS NOT NULL
	`)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	totals := map[string]protocol.ModelCost{}
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, unavailable(err)
		}
		var models map[string]protocol.ModelUsage
		if err := json.Unmarshal([]byte(encoded), &models); err != nil {
			// A malformed row is one attempt's breakdown, not a reason to fail
			// the whole Overview. The totals above are unaffected.
			continue
		}
		for model, usage := range models {
			entry := totals[model]
			entry.Model = model
			entry.CostUSD += usage.CostUSD
			entry.Attempts++
			totals[model] = entry
		}
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	byModel := make([]protocol.ModelCost, 0, len(totals))
	for _, entry := range totals {
		byModel = append(byModel, entry)
	}
	// Most expensive first, then by name so the order is stable when two
	// models cost the same.
	sort.Slice(byModel, func(i, j int) bool {
		if byModel[i].CostUSD != byModel[j].CostUSD {
			return byModel[i].CostUSD > byModel[j].CostUSD
		}
		return byModel[i].Model < byModel[j].Model
	})
	return byModel, nil
}
