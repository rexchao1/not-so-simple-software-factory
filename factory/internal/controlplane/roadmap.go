package controlplane

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The Roadmap is the Planning page's whole payload: every project's route, the
// checkpoints on it, what each frozen checkpoint was split into, and what is
// waiting on the human.
//
// It used to parse the orchestrator's files under a configured roadmap_root,
// and it now reads the planning tables migration 046 added. The JSON is
// unchanged on purpose. The page was built against this shape and reads it
// correctly, so the source moved and the contract did not.
//
// It is still a read and only a read. Nothing here writes a planning row; the
// writes are the /api/v1/planning routes, which the light-factory skill calls
// deliberately, and this file cannot be made to trigger one.
const (
	// roadmapMaxSummary caps a pebble's derived summary, which is a card
	// subtitle on the page and not the task body.
	roadmapMaxSummary = planningMaxSummaryBytes
)

// RoadmapPebble is one unit of work a checkpoint was split into. State, WorkID
// and PullRequestURL are joined in from the factory's own Work rows rather
// than stored on the pebble, so a pebble can say whether it was actually built
// rather than only that it was planned. An unjoined pebble has an empty state,
// which is different from a pebble whose build failed.
//
// A pebble that carries a saved work id is joined by that id. One that does
// not is joined by title, which is what the pebble was submitted as. The id is
// exact and the title is a guess, so the id wins wherever both exist.
//
// A pebble marked built by hand starts out with state "built" and carries
// BuiltRef, the commit or pull request it names. A Work row found for the
// same pebble overwrites both the state and, when the run has its own
// pull request, effectively replaces it: a real run is stronger evidence
// than a hand entry.
type RoadmapPebble struct {
	Ordinal        int    `json:"ordinal"`
	Slug           string `json:"slug"`
	Title          string `json:"title"`
	Summary        string `json:"summary,omitempty"`
	State          string `json:"state,omitempty"`
	WorkID         string `json:"work_id,omitempty"`
	PullRequestURL string `json:"pull_request_url,omitempty"`
	BuiltRef       string `json:"built_ref,omitempty"`
}

// RoadmapStone is one big chunk of work inside a checkpoint: the answer to
// "what is this checkpoint made of". A checkpoint whose pebbles were cut
// before anyone grouped them, or whose split named a stone for some pebbles
// and not others, still shows every pebble, because ungrouped pebbles fall
// into a trailing catch-all. Nothing is hidden by a grouping.
type RoadmapStone struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Statement string          `json:"statement,omitempty"`
	Pebbles   []RoadmapPebble `json:"pebbles"`
	State     string          `json:"state"`
}

// RoadmapPass is one planning pass against a checkpoint: at this point in the
// shift, a critique. Cost is what that single pass cost, which is the number
// that says whether a critic that found nothing was worth running.
//
// A pass is a Work row on the critique pipeline, found by the planning triple
// admission stamps on its session. Outcome is that Work's own state, so a
// critique that failed reads as a failed pass rather than disappearing. See
// roadmap_passes.go.
//
// WorkID is that Work row's id. The page draws a pass as a line the operator
// can open, and everything a pass produced beyond these six numbers, the
// findings themselves, the attempts, the failure, lives on the Work and
// nowhere else. Without the id the page can name a pass and then has nowhere
// to send anyone who wants to read it.
type RoadmapPass struct {
	At         time.Time `json:"at"`
	Mode       string    `json:"mode"`
	Round      int       `json:"round"`
	Model      string    `json:"model,omitempty"`
	CostUSD    float64   `json:"cost_usd"`
	DurationMS int       `json:"duration_ms,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	WorkID     string    `json:"work_id,omitempty"`
}

// RoadmapLivePass is a pass that is turning at this moment: a critique Work
// item that is running. It used to be a marker file the orchestrator held for
// as long as its model ran, with all the staleness that implies when the pass
// was killed before it could clean up. A running Work row cannot go stale that
// way, because the factory owns the lifecycle that ends it.
//
// It is the critique Work item whose state says it is turning: queued,
// preparing or running. WorkID is that Work row, so the one thing a human
// wants when they see something turning, which is to watch it, is a link
// rather than a hunt through the Work board for a task with the right name.
type RoadmapLivePass struct {
	Mode    string    `json:"mode"`
	Round   int       `json:"round"`
	Model   string    `json:"model,omitempty"`
	Started time.Time `json:"started"`
	WorkID  string    `json:"work_id,omitempty"`
}

// RoadmapCheckpoint is one rung of a project's route, and the unit that has a
// written PRD. Stones is the grouped view of the same pebbles Pebbles holds
// flat; both are sent because the page reads the grouping and the counts read
// the flat list.
type RoadmapCheckpoint struct {
	Number     int              `json:"number"`
	Title      string           `json:"title"`
	Summary    string           `json:"summary,omitempty"`
	Status     string           `json:"status"`
	Planned    bool             `json:"planned"`
	Stones     []RoadmapStone   `json:"stones"`
	Pebbles    []RoadmapPebble  `json:"pebbles"`
	Passes     []RoadmapPass    `json:"passes"`
	Live       *RoadmapLivePass `json:"live,omitempty"`
	CostUSD    float64          `json:"cost_usd"`
	PassRounds int              `json:"pass_rounds"`
	// answered says whether review answers have been saved against this
	// checkpoint. It is unexported, so it does not serialize and the page's
	// shape is unchanged: the page needs the derived Waiting entry, not the
	// input that produced it, and sending both would let the two disagree.
	answered bool
}

// RoadmapProject is one project's route: the thing the human said they wanted,
// and the checkpoints that get there.
type RoadmapProject struct {
	Project     string              `json:"project"`
	Title       string              `json:"title"`
	Statement   string              `json:"statement,omitempty"`
	Checkpoints []RoadmapCheckpoint `json:"checkpoints"`
	Live        *RoadmapLivePass    `json:"live,omitempty"`
	CostUSD     float64             `json:"cost_usd"`
	BuiltCount  int                 `json:"built_count"`
}

// RoadmapWaiting is a checkpoint that cannot move until the human does
// something. It is derived on every read, never stored, so it cannot go stale
// against the rows it was derived from.
type RoadmapWaiting struct {
	Project    string  `json:"project"`
	Number     int     `json:"number"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	Reason     string  `json:"reason"`
	Action     string  `json:"action"`
	CostUSD    float64 `json:"cost_usd"`
	PassRounds int     `json:"pass_rounds"`
}

// Roadmap is the whole response. Configured is now true whenever the store is
// open, because planning lives in that store and there is no root to point at
// and no way to have one without the other. It stays in the shape so the page
// keeps compiling against it and so a future reason to report an unusable
// planning source has somewhere to live.
type Roadmap struct {
	Configured bool             `json:"configured"`
	Projects   []RoadmapProject `json:"projects"`
	Waiting    []RoadmapWaiting `json:"waiting"`
	ReadAt     time.Time        `json:"read_at"`
}

var (
	roadmapPebbleTitle   = regexp.MustCompile(`(?m)^#{1,3}\s+(.+?)\s*$`)
	roadmapPebbleSection = regexp.MustCompile(`(?m)^#{2,4}\s+What are we building\?\s*$`)
)

// readRoadmap builds the whole roadmap from the planning tables.
//
// A storage failure is returned rather than swallowed. The file reader used to
// skip an unreadable project so one bad route file could not hide the rest,
// but a failing query is not one bad project: it is the database, and showing
// an empty Planning page in that case would say "you have planned nothing"
// when the truth is "I could not look".
func readRoadmap(ctx context.Context, store *Store) (Roadmap, error) {
	roadmap := Roadmap{
		ReadAt:   time.Now().UTC(),
		Projects: []RoadmapProject{},
		Waiting:  []RoadmapWaiting{},
	}
	if store == nil {
		return roadmap, nil
	}
	roadmap.Configured = true
	projects, err := store.PlanningProjects(ctx)
	if err != nil {
		return Roadmap{}, err
	}
	for _, project := range projects {
		read, err := readRoadmapProject(ctx, store, project)
		if err != nil {
			return Roadmap{}, err
		}
		roadmap.Projects = append(roadmap.Projects, read)
	}
	passes, err := readRoadmapPasses(ctx, store)
	if err != nil {
		return Roadmap{}, err
	}
	// Passes are applied before the waiting list is derived, because a waiting
	// entry carries its checkpoint's cost and round count and would otherwise
	// report both as zero for a checkpoint that has been critiqued twice.
	roadmapApplyPasses(&roadmap, passes)
	roadmap.Waiting = roadmapWaiting(roadmap.Projects)
	return roadmap, nil
}

func readRoadmapProject(
	ctx context.Context, store *Store, project PlanningProject,
) (RoadmapProject, error) {
	read := RoadmapProject{
		Project:     project.Project,
		Title:       project.Title,
		Statement:   project.Statement,
		Checkpoints: []RoadmapCheckpoint{},
	}
	numbers, err := store.planningCheckpointNumbers(ctx, project.Project)
	if err != nil {
		return RoadmapProject{}, err
	}
	for _, number := range numbers {
		stored, err := store.PlanningCheckpoint(ctx, project.Project, number)
		if err != nil {
			return RoadmapProject{}, err
		}
		checkpoint := roadmapCheckpoint(stored)
		if checkpoint.Status == "built" {
			read.BuiltCount++
		}
		// Cost is not summed here. A checkpoint's cost is the cost of its
		// passes, which roadmapApplyPasses has not read yet, and it rolls the
		// project total up as it stamps each checkpoint.
		read.Checkpoints = append(read.Checkpoints, checkpoint)
	}
	return read, nil
}

// roadmapCheckpoint turns one stored checkpoint into the page's shape.
//
// Planned means a PRD has been written, which is what the page uses to tell a
// route line from a checkpoint that exists as a document. The file reader
// answered it by whether <n>.md was on disk; the table answers it by whether
// the body is empty, which is the same question without the filesystem.
func roadmapCheckpoint(stored PlanningCheckpoint) RoadmapCheckpoint {
	checkpoint := RoadmapCheckpoint{
		Number:   stored.Number,
		Title:    stored.Title,
		Summary:  stored.Summary,
		Status:   stored.Status,
		Planned:  strings.TrimSpace(stored.Body) != "",
		answered: strings.TrimSpace(stored.Answers) != "",
		Pebbles:  make([]RoadmapPebble, 0, len(stored.Pebbles)),
		// Non-nil so a checkpoint that has never been critiqued carries an
		// empty array rather than null, which is what the page reads.
		// roadmapApplyPasses replaces it where passes exist.
		Passes: []RoadmapPass{},
	}
	if checkpoint.Title == "" {
		checkpoint.Title = fmt.Sprintf("Checkpoint %d", stored.Number)
	}
	byStone := map[string][]RoadmapPebble{}
	for _, stored := range stored.Pebbles {
		pebble := RoadmapPebble{
			Ordinal: stored.Ordinal,
			Slug:    stored.Slug,
			Title:   stored.Title,
			Summary: roadmapPebbleSummary(stored.Body),
			WorkID:  stored.WorkID,
		}
		// built_at with no Work row yet is what a hand-built pebble looks like:
		// there is no run to join it to, so this is the only evidence of state
		// the roadmap has until roadmapApplyWork runs and, if it finds a real
		// Work row, overwrites it.
		if stored.BuiltAt != nil {
			pebble.State = "built"
			pebble.BuiltRef = stored.BuiltRef
		}
		checkpoint.Pebbles = append(checkpoint.Pebbles, pebble)
		byStone[stored.StoneID] = append(byStone[stored.StoneID], pebble)
	}
	checkpoint.Stones = roadmapStones(stored.Stones, checkpoint.Pebbles, byStone)
	return checkpoint
}

// roadmapStones groups a checkpoint's pebbles the way its split said to.
// Every pebble reaches the page exactly once. A stone that ended up holding
// nothing is dropped rather than drawn as an empty box, and pebbles that named
// no stone go into a trailing catch-all, so a checkpoint split before anyone
// grouped it still renders as one stone holding everything.
func roadmapStones(
	stored []PlanningStone, all []RoadmapPebble, byStone map[string][]RoadmapPebble,
) []RoadmapStone {
	stones := []RoadmapStone{}
	if len(all) == 0 {
		return stones
	}
	for _, entry := range stored {
		pebbles := byStone[entry.ID]
		if len(pebbles) == 0 {
			continue
		}
		stones = append(stones, RoadmapStone{
			ID:        entry.ID,
			Title:     entry.Title,
			Statement: entry.Statement,
			Pebbles:   pebbles,
		})
	}
	if rest := byStone[""]; len(rest) > 0 {
		catchAll := RoadmapStone{
			ID:      fmt.Sprintf("B%d", len(stones)+1),
			Title:   "The rest of the checkpoint",
			Pebbles: rest,
		}
		if len(stones) == 0 {
			catchAll.Title = "Everything in this checkpoint"
		}
		stones = append(stones, catchAll)
	}
	for i := range stones {
		stones[i].State = roadmapRollUp(stones[i].Pebbles)
	}
	return stones
}

// planningCheckpointNumbers lists one project's checkpoints in route order.
func (s *Store) planningCheckpointNumbers(ctx context.Context, project string) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT number FROM planning_checkpoints WHERE project = ? ORDER BY number LIMIT ?
	`, project, planningMaxCheckpointNumber)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	numbers := []int{}
	for rows.Next() {
		var number int
		if err := rows.Scan(&number); err != nil {
			return nil, unavailable(err)
		}
		numbers = append(numbers, number)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return numbers, nil
}

// roadmapPebbleSummary is the first paragraph of a pebble's opening section,
// which is where the task shape says what is being built. It is derived rather
// than stored because it is a rendering of the body, and a stored copy would
// be one more thing that can disagree with the text it summarises. It is
// capped because this is a card subtitle, not the task.
func roadmapPebbleSummary(body string) string {
	rest := body
	if idx := roadmapPebbleSection.FindStringIndex(rest); idx != nil {
		rest = rest[idx[1]:]
	} else if idx := roadmapPebbleTitle.FindStringIndex(rest); idx != nil {
		rest = rest[idx[1]:]
	}
	for _, block := range strings.Split(rest, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, "#") {
			continue
		}
		block = strings.Join(strings.Fields(block), " ")
		if len(block) > roadmapMaxSummary {
			block = strings.TrimSpace(block[:roadmapMaxSummary]) + "..."
		}
		return block
	}
	return ""
}

// roadmapWaiting is the whole point of the page: the checkpoints that stopped
// because they need the human, and nothing else. A checkpoint the factory is
// still building is not waiting on anyone and does not appear.
//
// Two reasons, and only two. A checkpoint in review with no answers saved is
// waiting to be read. A checkpoint in fog stopped on questions the repository
// could not answer. A frozen checkpoint with no pebbles used to be a third
// reason, back when splitting was a separate command a human had to remember
// to run; the light factory cuts pebbles in the same context that froze the
// PRD, so a frozen checkpoint is the agent's turn and not the human's.
func roadmapWaiting(projects []RoadmapProject) []RoadmapWaiting {
	waiting := []RoadmapWaiting{}
	for _, project := range projects {
		for _, checkpoint := range project.Checkpoints {
			reason, action := "", ""
			switch checkpoint.Status {
			case "review":
				if checkpoint.answered {
					continue
				}
				reason = "The plan is written and waiting for your answers. It cannot become tasks until you review it."
				action = "Review the plan"
			case "fog":
				reason = "Drafting stopped on questions it could not answer from the repository."
				action = "Answer the questions"
			}
			if reason == "" {
				continue
			}
			waiting = append(waiting, RoadmapWaiting{
				Project:    project.Project,
				Number:     checkpoint.Number,
				Title:      checkpoint.Title,
				Status:     checkpoint.Status,
				Reason:     reason,
				Action:     action,
				CostUSD:    checkpoint.CostUSD,
				PassRounds: checkpoint.PassRounds,
			})
		}
	}
	return waiting
}
