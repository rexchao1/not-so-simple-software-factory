package controlplane

import (
	"context"
	"sort"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// roadmapWorkPages is how many pages of work rows the join reads. The page size
// is the store's maximum, so this is a few hundred of the most recent runs:
// enough to cover every pebble anyone is currently building, and bounded so a
// long-lived factory does not turn one page view into a full table scan.
const roadmapWorkPages = 3

// roadmapWorkState is the join between a planned pebble and the factory's own
// runs. A pebble becomes a submitted task under its own title, so the title is
// one key. It is a plain string match on the trimmed title because that is
// what the skill actually sends, and a fuzzier rule would colour a stone
// from somebody else's work.
type roadmapWorkState struct {
	State          string
	WorkID         string
	PullRequestURL string
}

// roadmapWorkJoin is the two ways a pebble reaches its Work row.
//
// ByID is exact: the pebble recorded the Work id it was admitted as, so there
// is nothing to guess. ByName is the fallback for a pebble planned before it
// was submitted, or submitted by something that did not write the id back.
// Both are kept because dropping the name join would silently un-colour every
// pebble already on the board, and dropping the id join would leave two
// pebbles that share a title fighting over one Work row.
type roadmapWorkJoin struct {
	ByID   map[string]roadmapWorkState
	ByName map[string]roadmapWorkState
}

func (j roadmapWorkJoin) empty() bool { return len(j.ByID) == 0 && len(j.ByName) == 0 }

// roadmapWorkIndex reads recent work and indexes it by Work id and by task
// name. When one name has several runs, the newest wins, because a pebble that
// failed and was rerun should show what it is doing now, not what it did
// first. WorkPage returns newest first, so the first row seen for a name is
// the newest one. Ids are unique, so that rule only bites the name index.
func roadmapWorkIndex(ctx context.Context, store *Store) roadmapWorkJoin {
	index := roadmapWorkJoin{
		ByID:   map[string]roadmapWorkState{},
		ByName: map[string]roadmapWorkState{},
	}
	if store == nil {
		return index
	}
	cursor := ""
	for page := 0; page < roadmapWorkPages; page++ {
		result, err := store.WorkPage(ctx, protocol.WorkFilter{}, maxTaskPageSize, cursor)
		if err != nil {
			return index
		}
		for _, work := range result.Work {
			state := roadmapWorkState{
				State:          string(work.State),
				WorkID:         work.ID,
				PullRequestURL: work.PullRequestURL,
			}
			if work.ID != "" {
				index.ByID[work.ID] = state
			}
			name := strings.TrimSpace(work.TaskName)
			if name == "" {
				continue
			}
			if _, seen := index.ByName[name]; seen {
				continue
			}
			index.ByName[name] = state
		}
		cursor = result.NextCursor
		if cursor == "" {
			break
		}
	}
	return index
}

// roadmapApplyWork stamps every pebble with the state of the run that built it,
// then rolls each stone up from its pebbles. The planning tables say what is
// planned; this is what makes the page say what is happening.
func roadmapApplyWork(roadmap *Roadmap, index roadmapWorkJoin) {
	if roadmap == nil || index.empty() {
		return
	}
	for p := range roadmap.Projects {
		project := &roadmap.Projects[p]
		for c := range project.Checkpoints {
			checkpoint := &project.Checkpoints[c]
			for i := range checkpoint.Pebbles {
				roadmapStampPebble(&checkpoint.Pebbles[i], index)
			}
			for b := range checkpoint.Stones {
				stone := &checkpoint.Stones[b]
				for i := range stone.Pebbles {
					roadmapStampPebble(&stone.Pebbles[i], index)
				}
				stone.State = roadmapRollUp(stone.Pebbles)
			}
		}
	}
}

// roadmapStampPebble joins one pebble to its Work row, by the id the pebble
// carries if it has one and by title otherwise. A pebble with a work id that
// matches nothing in the recent pages keeps the id and takes no state: the
// Work is real, it is just older than the join reads, and inventing a title
// match for it would be a guess dressed as a fact.
func roadmapStampPebble(pebble *RoadmapPebble, index roadmapWorkJoin) {
	if pebble.WorkID != "" {
		if work, ok := index.ByID[pebble.WorkID]; ok {
			pebble.State = work.State
			pebble.PullRequestURL = work.PullRequestURL
		}
		return
	}
	work, ok := index.ByName[strings.TrimSpace(pebble.Title)]
	if !ok {
		return
	}
	pebble.State = work.State
	pebble.WorkID = work.WorkID
	pebble.PullRequestURL = work.PullRequestURL
}

// roadmapRollUp turns a stone's pebble states into the one word the page
// colours the box by. Trouble outranks progress and progress outranks done, so
// a stone never looks finished while part of it is broken or still moving.
//
//	working  something is running, queued or waiting on an answer right now
//	failed   nothing is moving and at least one pebble failed or was cancelled
//	done     every pebble reached a terminal success, built by hand or by a run
//	part     some pebbles are done and the rest have not started
//	planned  no pebble has a run yet
func roadmapRollUp(pebbles []RoadmapPebble) string {
	if len(pebbles) == 0 {
		return "planned"
	}
	active, failed, done := 0, 0, 0
	for _, pebble := range pebbles {
		switch pebble.State {
		case "queued", "blocked", "preparing", "running", "needs-input", "ready":
			active++
		case "failed", "cancelled":
			failed++
		case "succeeded", "no-change", "built":
			done++
		}
	}
	switch {
	case active > 0:
		return "working"
	case failed > 0:
		return "failed"
	case done == len(pebbles):
		return "done"
	case done > 0:
		return "part"
	default:
		return "planned"
	}
}

// roadmapSortWaiting keeps the bell's list in a stable order so the count and
// the list agree between two reads of the same state.
func roadmapSortWaiting(waiting []RoadmapWaiting) {
	sort.SliceStable(waiting, func(i, j int) bool {
		if waiting[i].Project != waiting[j].Project {
			return waiting[i].Project < waiting[j].Project
		}
		return waiting[i].Number < waiting[j].Number
	})
}
