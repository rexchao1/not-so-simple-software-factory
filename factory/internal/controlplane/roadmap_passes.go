package controlplane

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

// A pass is one critique of one checkpoint. It is a Work row and nothing else:
// the orchestrator used to record passes in a ledger file and hold a marker
// file open for the duration of a running one, and both could disagree with
// what had actually run. A pass is now read from the session that ran it, so
// the cost is the cost the runtime reported, the duration is measured from the
// timestamps the lifecycle wrote, and a pass that was killed cannot leave a
// stale marker behind because there is no marker.
//
// The join is the planning triple migration 047 put on sessions. Admission is
// the only writer of it and refuses a partial triple, so a session either is a
// pass on a known checkpoint or is not a pass at all.

// roadmapPassModeCritique is the only mode there is. The field is kept because
// the Planning page reads it and because the collapse of the planning chain
// into one context is what removed the other modes; a future non-interactive
// pass would be a second value here rather than a second shape.
const roadmapPassModeCritique = "critique"

// roadmapMaxPasses bounds the read. A checkpoint has a handful of critique
// rounds and an operator has tens of checkpoints, so this is far above any
// real planning history and still keeps one page view from reading an
// unbounded slice of the sessions table.
const roadmapMaxPasses = 1000

// roadmapCheckpointPasses is everything one checkpoint's passes contribute to
// the page: the list, the rolled-up cost, the number of rounds it has had, and
// the one pass that is turning right now.
type roadmapCheckpointPasses struct {
	Passes  []RoadmapPass
	Live    *RoadmapLivePass
	CostUSD float64
	Rounds  int
}

type roadmapPassKey struct {
	Project string
	Number  int
}

// roadmapPassLive reports whether a Work state means the pass is turning.
//
// Queued and preparing count alongside running because from the human's side
// the pass has been submitted and has not come back, which is the question the
// live indicator answers. Draft, blocked and needs-input deliberately do not:
// each is waiting on a person or on capacity, and drawing them as live would
// say the model is working when nothing is.
func roadmapPassLive(state string) bool {
	switch protocol.SessionState(state) {
	case protocol.SessionQueued, protocol.SessionPreparing, protocol.SessionRunning:
		return true
	default:
		return false
	}
}

// readRoadmapPasses reads every critique pass, grouped by checkpoint.
func readRoadmapPasses(
	ctx context.Context, store *Store,
) (map[roadmapPassKey]*roadmapCheckpointPasses, error) {
	index := map[roadmapPassKey]*roadmapCheckpointPasses{}
	if store == nil {
		return index, nil
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT session.id,
		       session.planning_project, session.planning_number, session.planning_round,
		       session.state, session.execution_model,
		       session.admitted_at, session.started_at, session.terminal_at,
		       (SELECT COALESCE(SUM(attempt.cost_usd), 0)
		          FROM attempts attempt
		          JOIN executions execution ON execution.id = attempt.execution_id
		         WHERE execution.session_id = session.id)
		FROM sessions session
		WHERE session.planning_project IS NOT NULL
		  AND session.planning_number IS NOT NULL
		  AND session.planning_round IS NOT NULL
		ORDER BY session.planning_project, session.planning_number,
		         session.planning_round, session.admitted_at, session.id
		LIMIT ?
	`, roadmapMaxPasses)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	rounds := map[roadmapPassKey]map[int]struct{}{}
	for rows.Next() {
		var key roadmapPassKey
		var round int
		var workID, state, model string
		var admitted int64
		var started, terminal sql.NullInt64
		var cost float64
		if err := rows.Scan(&workID, &key.Project, &key.Number, &round, &state, &model,
			&admitted, &started, &terminal, &cost); err != nil {
			return nil, unavailable(err)
		}
		entry := index[key]
		if entry == nil {
			entry = &roadmapCheckpointPasses{Passes: []RoadmapPass{}}
			index[key] = entry
			rounds[key] = map[int]struct{}{}
		}
		pass := RoadmapPass{
			At:      roadmapPassAt(admitted, started, terminal),
			Mode:    roadmapPassModeCritique,
			Round:   round,
			Model:   model,
			CostUSD: cost,
			Outcome: state,
			WorkID:  workID,
		}
		if started.Valid && terminal.Valid && terminal.Int64 >= started.Int64 {
			pass.DurationMS = int(terminal.Int64 - started.Int64)
		}
		entry.Passes = append(entry.Passes, pass)
		entry.CostUSD += cost
		rounds[key][round] = struct{}{}
		// The newest live pass wins. The order above is by round and then by
		// admission, so a re-submitted round replaces the one it retried
		// rather than leaving the page pointing at an abandoned attempt.
		if roadmapPassLive(state) {
			entry.Live = &RoadmapLivePass{
				Mode:    roadmapPassModeCritique,
				Round:   round,
				Model:   model,
				Started: roadmapPassStarted(admitted, started),
				WorkID:  workID,
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	for key, entry := range index {
		entry.Rounds = len(rounds[key])
	}
	return index, nil
}

// roadmapPassAt is when the pass happened. A finished pass is dated by when it
// finished, because that is when its findings existed; one still turning is
// dated by when it started, and one that has not started yet by when it was
// admitted. The field is never zero, so the page never has to render an
// unstamped row.
func roadmapPassAt(admitted int64, started, terminal sql.NullInt64) time.Time {
	if terminal.Valid {
		return fromMillis(terminal.Int64)
	}
	if started.Valid {
		return fromMillis(started.Int64)
	}
	return fromMillis(admitted)
}

// roadmapPassStarted is when a live pass began. A queued pass has no start
// time yet, so it falls back to admission rather than reporting the zero time,
// which the page would draw as 1970.
func roadmapPassStarted(admitted int64, started sql.NullInt64) time.Time {
	if started.Valid {
		return fromMillis(started.Int64)
	}
	return fromMillis(admitted)
}

// roadmapApplyPasses stamps each checkpoint with its passes and rolls the cost
// and the round count up to the checkpoint, and from there to the project.
//
// PassRounds counts distinct rounds rather than passes: a round re-submitted
// after a failure is still one round of review, and reporting it as two would
// tell the human the PRD had been criticised more times than it had. Cost sums
// every pass, retries included, because the money was spent either way.
func roadmapApplyPasses(roadmap *Roadmap, index map[roadmapPassKey]*roadmapCheckpointPasses) {
	if roadmap == nil || len(index) == 0 {
		return
	}
	for p := range roadmap.Projects {
		project := &roadmap.Projects[p]
		for c := range project.Checkpoints {
			checkpoint := &project.Checkpoints[c]
			entry := index[roadmapPassKey{Project: project.Project, Number: checkpoint.Number}]
			if entry == nil {
				continue
			}
			passes := append([]RoadmapPass(nil), entry.Passes...)
			sort.SliceStable(passes, func(i, j int) bool {
				if passes[i].Round != passes[j].Round {
					return passes[i].Round < passes[j].Round
				}
				return passes[i].At.Before(passes[j].At)
			})
			checkpoint.Passes = passes
			checkpoint.Live = entry.Live
			checkpoint.CostUSD = entry.CostUSD
			checkpoint.PassRounds = entry.Rounds
			project.CostUSD += entry.CostUSD
			// The project's live pass is the first one found in route order.
			// Only one checkpoint is normally being critiqued at a time, and
			// when two are, the earlier rung is the one the human is closer to
			// finishing.
			if project.Live == nil {
				project.Live = entry.Live
			}
		}
	}
}
