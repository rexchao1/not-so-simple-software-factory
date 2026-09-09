package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Planning state is the light factory's half of the product: the route a
// project takes, the PRD for each checkpoint on it, the answers a human gave
// at review, and the stones and pebbles a frozen checkpoint was cut into.
//
// It used to live as files in the orchestrator's own directory, which the
// cockpit read and nothing wrote. Two machines editing those files is what
// broke, so the store became the only writer. Everything below is a validated
// write in the same shape as admission: normalize, bound, refuse with a stable
// code, and let the caller see exactly which field was wrong.
//
// The store owns three rules that no caller can talk it out of.
//
//  1. A frozen or built checkpoint's PRD never changes again. Freezing is the
//     promise the whole light-factory chain is built on: the human said yes to
//     a specific text, and the pebbles cut from it cite that text. Answers are
//     the one exception, because they are a record of what was said and not a
//     claim about what will be built.
//  2. Status moves along one path and no other. See planningTransitions.
//  3. Pebbles only exist under a frozen checkpoint. Cutting tasks from a plan
//     that can still change is how you get four pull requests against a spec
//     that no longer says that.

const (
	// planningMaxProjects bounds the list endpoint. This is one operator's own
	// planning, not a multi-tenant catalogue.
	planningMaxProjects = 64
	// planningMaxCheckpointNumber matches the route line format, which numbers
	// checkpoints from 1 and never reaches three digits in practice.
	planningMaxCheckpointNumber = 999
	planningMaxSlugBytes        = 64
	planningMaxTitleBytes       = 200
	// planningMaxSummaryBytes is the card subtitle on the Planning page. It is
	// the same bound the file reader applied when it cut a summary out of a
	// route line, so a summary that rendered before still renders.
	planningMaxSummaryBytes   = 240
	planningMaxStatementBytes = 4096
	planningMaxRouteBytes     = 64 << 10
	// planningMaxBodyBytes is one checkpoint PRD. Generous, because a PRD with
	// every decision cited is long, and still an order of magnitude under the
	// 1 MiB request cap so the body and its metadata fit in one PUT.
	planningMaxBodyBytes = 128 << 10
	// planningMaxAnswersBytes is the accumulated review answers for one
	// checkpoint, several lavish rounds' worth.
	planningMaxAnswersBytes = 64 << 10
	planningMaxPebbles      = 100
	planningMaxStones       = 50
	// planningMaxPebbleBodyBytes is one factory task. Sized so a full batch of
	// planningMaxPebbles fits inside protocol.MaxBodyBytes with room for the
	// stones alongside it, because the batch is a single request by design:
	// a checkpoint's pebbles are replaced all at once or not at all.
	planningMaxPebbleBodyBytes = 8 << 10
	// planningMaxBuiltRefBytes bounds a built pebble's ref: a commit sha or a
	// pull request URL, neither of which comes close to this.
	planningMaxBuiltRefBytes = 512
)

// planningSlugPattern is the one identifier shape. Projects, pebble slugs and
// stone ids all use it: lowercase, digits, and single separators, starting
// and ending on a word character. It is deliberately narrow, because these
// names reach a URL path, a filename in a worktree, and a human's eye.
var planningSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

// PlanningStatuses is the closed set of checkpoint statuses, in the order a
// checkpoint travels through them. Migration 046 constrains the column to the
// same five, so a status that reaches the database has already passed here.
var PlanningStatuses = []string{"planned", "review", "fog", "frozen", "built"}

// planningTransitions is the whole state machine. Every move not named here is
// refused with planning_transition_not_allowed, including a move to the status
// a checkpoint already holds: re-freezing a frozen checkpoint is either a bug
// or a lost update, and silently accepting it would hide both.
//
//	planned -> review   a PRD was written and is offered for review
//	review  -> fog      the review found questions the repository cannot answer
//	review  -> frozen   the human said freeze
//	fog     -> frozen   the questions were answered and the human said freeze
//	frozen  -> built    every pebble cut from it was delivered
//
// There is no way back. A frozen checkpoint that turned out wrong becomes a
// new checkpoint on the route, which is what the route is for; rewinding one
// would silently invalidate the pebbles already cut from its text.
var planningTransitions = map[string][]string{
	"planned": {"review"},
	"review":  {"fog", "frozen"},
	"fog":     {"frozen"},
	"frozen":  {"built"},
	"built":   nil,
}

// PlanningProject is one project's route: the boulder statement and the
// markdown route body, keyed by the slug every command names it with.
type PlanningProject struct {
	Project     string    `json:"project"`
	Title       string    `json:"title"`
	Statement   string    `json:"statement,omitempty"`
	Route       string    `json:"route,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Checkpoints int       `json:"checkpoints"`
}

// SavePlanningProjectRequest is the body of PUT /planning/projects/{project}.
// It is a whole-resource replace: every field is written as given, so a field
// left out is cleared rather than kept. The light factory writes the route in
// one go from its own context, so a partial update would only be a way to lose
// half of it.
type SavePlanningProjectRequest struct {
	Title     string `json:"title"`
	Statement string `json:"statement"`
	Route     string `json:"route"`
}

// PlanningStone is one chunk of a checkpoint: the answer to "what is this
// checkpoint made of". It groups pebbles and carries no work of its own.
type PlanningStone struct {
	ID        string `json:"id"`
	Ordinal   int    `json:"ordinal"`
	Title     string `json:"title"`
	Statement string `json:"statement,omitempty"`
}

// PlanningPebble is one factory task cut from a frozen checkpoint. WorkID is
// set once the pebble has been admitted to the dark factory, which lets the
// roadmap join it to its Work row exactly rather than by title.
//
// BuiltAt and BuiltRef record a pebble built by hand, outside the factory:
// the moment and the commit or pull request that carries it. They are set
// through their own route, never through a split, and a pebble that also
// carries a Work row is still coloured by that Work, because a real run
// outranks a hand entry.
type PlanningPebble struct {
	Ordinal  int        `json:"ordinal"`
	Slug     string     `json:"slug"`
	Title    string     `json:"title"`
	Body     string     `json:"body,omitempty"`
	StoneID  string     `json:"stone_id,omitempty"`
	WorkID   string     `json:"work_id,omitempty"`
	BuiltAt  *time.Time `json:"built_at,omitempty"`
	BuiltRef string     `json:"built_ref,omitempty"`
}

// PlanningCheckpoint is one rung of a route with everything hanging off it.
// Stones and Pebbles are empty until the checkpoint is frozen and cut.
type PlanningCheckpoint struct {
	Project   string           `json:"project"`
	Number    int              `json:"number"`
	Title     string           `json:"title"`
	Summary   string           `json:"summary,omitempty"`
	Status    string           `json:"status"`
	Body      string           `json:"body,omitempty"`
	Answers   string           `json:"answers,omitempty"`
	FrozenAt  *time.Time       `json:"frozen_at,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Stones    []PlanningStone  `json:"stones"`
	Pebbles   []PlanningPebble `json:"pebbles"`
}

// SavePlanningCheckpointRequest is the body of a checkpoint PUT. Status is not
// on it on purpose: a status change is its own route with its own rules, and
// letting a body write move a checkpoint to frozen would route around them.
type SavePlanningCheckpointRequest struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Body    string `json:"body"`
}

// InsertPlanningCheckpointRequest is the body of the insert route: a new
// checkpoint pushed into the route at a chosen number, carrying the revised
// route markdown that now names it. It has no body field, unlike the plain
// checkpoint PUT, because inserting is how a checkpoint that was only just
// discovered gets a place on the route; its PRD is written afterward through
// the ordinary checkpoint PUT.
type InsertPlanningCheckpointRequest struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Route   string `json:"route"`
}

// SavePlanningAnswersRequest replaces the saved review answers. A replace and
// not an append, because the caller holds the whole conversation in its own
// context and appending would make the server's copy depend on how many times
// a flaky client retried.
type SavePlanningAnswersRequest struct {
	Answers string `json:"answers"`
}

// PlanningStatusRequest is the body of the status route.
type PlanningStatusRequest struct {
	Status string `json:"status"`
}

// MarkPlanningPebbleBuiltRequest is the body of the built route: the commit
// sha or pull request URL that carries a pebble someone built by hand.
type MarkPlanningPebbleBuiltRequest struct {
	Ref string `json:"ref"`
}

// SavePlanningPebblesRequest replaces a checkpoint's whole split in one batch.
// Pebbles name their stone by id rather than stones listing their pebbles,
// so there is exactly one representation of the link and no way for the two
// halves of the request to disagree.
type SavePlanningPebblesRequest struct {
	Stones  []PlanningStone  `json:"stones"`
	Pebbles []PlanningPebble `json:"pebbles"`
}

// UpsertPlanningProject creates or replaces one project's route.
func (s *Store) UpsertPlanningProject(
	ctx context.Context, project string, input SavePlanningProjectRequest,
) (PlanningProject, error) {
	project, err := validPlanningSlug(project, "invalid_planning_project", "project")
	if err != nil {
		return PlanningProject{}, err
	}
	title := strings.TrimSpace(input.Title)
	if err := boundPlanningText(title, planningMaxTitleBytes, "invalid_planning_title", "title"); err != nil {
		return PlanningProject{}, err
	}
	statement := strings.TrimSpace(input.Statement)
	if err := boundPlanningText(statement, planningMaxStatementBytes, "invalid_planning_statement", "statement"); err != nil {
		return PlanningProject{}, err
	}
	route := strings.TrimSpace(input.Route)
	if err := boundPlanningText(route, planningMaxRouteBytes, "invalid_planning_route", "route"); err != nil {
		return PlanningProject{}, err
	}
	// A project with no title reads as an untitled row on the Planning page.
	// The file reader fell back to the directory name for exactly this case,
	// and the same fallback keeps a project the skill saved in a hurry
	// readable.
	if title == "" {
		title = project
	}
	now := s.now().UTC().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO planning_projects(project, title, statement, route, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project) DO UPDATE SET
			title = excluded.title,
			statement = excluded.statement,
			route = excluded.route,
			updated_at = excluded.updated_at
	`, project, title, statement, route, now, now); err != nil {
		return PlanningProject{}, unavailable(err)
	}
	return s.PlanningProject(ctx, project)
}

// PlanningProject reads one project without its checkpoints.
func (s *Store) PlanningProject(ctx context.Context, project string) (PlanningProject, error) {
	project, err := validPlanningSlug(project, "invalid_planning_project", "project")
	if err != nil {
		return PlanningProject{}, err
	}
	var value PlanningProject
	var created, updated int64
	err = s.db.QueryRowContext(ctx, `
		SELECT p.project, p.title, p.statement, p.route, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM planning_checkpoints c WHERE c.project = p.project)
		FROM planning_projects p WHERE p.project = ?
	`, project).Scan(
		&value.Project, &value.Title, &value.Statement, &value.Route,
		&created, &updated, &value.Checkpoints,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanningProject{}, ErrNotFound
	}
	if err != nil {
		return PlanningProject{}, unavailable(err)
	}
	value.CreatedAt = fromMillis(created)
	value.UpdatedAt = fromMillis(updated)
	return value, nil
}

// PlanningProjects lists every project, newest route first is deliberately not
// the order: the Planning page shows a stable alphabetical list, because a
// project moving position when someone else saves a route is disorienting.
func (s *Store) PlanningProjects(ctx context.Context) ([]PlanningProject, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.project, p.title, p.statement, p.route, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM planning_checkpoints c WHERE c.project = p.project)
		FROM planning_projects p ORDER BY p.project LIMIT ?
	`, planningMaxProjects)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	projects := []PlanningProject{}
	for rows.Next() {
		var value PlanningProject
		var created, updated int64
		if err := rows.Scan(
			&value.Project, &value.Title, &value.Statement, &value.Route,
			&created, &updated, &value.Checkpoints,
		); err != nil {
			return nil, unavailable(err)
		}
		value.CreatedAt = fromMillis(created)
		value.UpdatedAt = fromMillis(updated)
		projects = append(projects, value)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return projects, nil
}

// UpsertPlanningCheckpoint writes a checkpoint's title, summary and PRD body.
//
// It refuses on a frozen or built checkpoint. That refusal is the point of
// freezing: the pebbles cut from a frozen PRD cite its text, and a chain that
// could still edit that text would let a task be built against a spec nobody
// approved. Answers are saved through their own route, which is allowed at any
// status, because recording what a human said is not a change to the plan.
//
// A new checkpoint starts at planned. Status is never moved here.
func (s *Store) UpsertPlanningCheckpoint(
	ctx context.Context, project string, number int, input SavePlanningCheckpointRequest,
) (PlanningCheckpoint, error) {
	project, number, err := validPlanningCheckpointKey(project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	title := strings.TrimSpace(input.Title)
	if err := boundPlanningText(title, planningMaxTitleBytes, "invalid_planning_title", "title"); err != nil {
		return PlanningCheckpoint{}, err
	}
	summary := strings.TrimSpace(input.Summary)
	if err := boundPlanningText(summary, planningMaxSummaryBytes, "invalid_planning_summary", "summary"); err != nil {
		return PlanningCheckpoint{}, err
	}
	body := strings.TrimSpace(input.Body)
	if err := boundPlanningText(body, planningMaxBodyBytes, "invalid_planning_body", "body"); err != nil {
		return PlanningCheckpoint{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	defer tx.Rollback()
	if err := planningProjectExists(ctx, tx, project); err != nil {
		return PlanningCheckpoint{}, err
	}
	status, found, err := planningCheckpointStatus(ctx, tx, project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	if found && (status == "frozen" || status == "built") {
		return PlanningCheckpoint{}, conflict(
			"planning_checkpoint_frozen",
			"a "+status+" checkpoint's plan cannot be edited; only its answers can still be saved",
		)
	}
	now := s.now().UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO planning_checkpoints(
			project, number, title, summary, status, body, answers, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'planned', ?, '', ?, ?)
		ON CONFLICT(project, number) DO UPDATE SET
			title = excluded.title,
			summary = excluded.summary,
			body = excluded.body,
			updated_at = excluded.updated_at
	`, project, number, title, summary, body, now, now); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	return s.PlanningCheckpoint(ctx, project, number)
}

// InsertPlanningCheckpoint creates a checkpoint at number and shifts every
// checkpoint at or above it, with its stones and pebbles, up by one. The
// project's route is replaced by the route carried in the same request, in
// the same transaction, so the markdown and the rows can never disagree.
//
// It refuses the shift if any checkpoint from number upward is frozen or
// built. A checkpoint's number is the thing the pebbles cut from it and the
// route line pointing at it both cite, and renumbering a frozen or built one
// out from under those citations is the exact edit freezing exists to
// prevent. Naming a mid-route number for a checkpoint that has not yet
// reached that promise is fine, which is why the check is frozen-or-built and
// not merely built.
func (s *Store) InsertPlanningCheckpoint(
	ctx context.Context, project string, number int, input InsertPlanningCheckpointRequest,
) (PlanningCheckpoint, error) {
	project, err := validPlanningSlug(project, "invalid_planning_project", "project")
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	title := strings.TrimSpace(input.Title)
	if err := boundPlanningText(title, planningMaxTitleBytes, "invalid_planning_title", "title"); err != nil {
		return PlanningCheckpoint{}, err
	}
	summary := strings.TrimSpace(input.Summary)
	if err := boundPlanningText(summary, planningMaxSummaryBytes, "invalid_planning_summary", "summary"); err != nil {
		return PlanningCheckpoint{}, err
	}
	route := strings.TrimSpace(input.Route)
	if err := boundPlanningText(route, planningMaxRouteBytes, "invalid_planning_route", "route"); err != nil {
		return PlanningCheckpoint{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	defer tx.Rollback()
	if err := planningProjectExists(ctx, tx, project); err != nil {
		return PlanningCheckpoint{}, err
	}
	highest, err := planningHighestCheckpointNumber(ctx, tx, project)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	if number < 1 || number > highest+1 {
		return PlanningCheckpoint{}, invalid("planning_invalid_checkpoint_number",
			fmt.Sprintf("a checkpoint can be inserted at a number from 1 to %d here", highest+1))
	}
	blocking, err := planningLowestBlockingCheckpoint(ctx, tx, project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	if blocking > 0 {
		return PlanningCheckpoint{}, conflict("planning_insert_not_clean",
			fmt.Sprintf("checkpoint %d is frozen or built and cannot be shifted", blocking))
	}
	// Deferred within this transaction only, and only for the duration of the
	// shift below: SQLite reverts it to off at the transaction's end on its
	// own. Every checkpoint, stone and pebble at or above number is a child of
	// its own checkpoint row through (project, number), and the shift moves
	// both halves of that key together but in separate statements, so without
	// deferring, the first statement to touch either side would find the other
	// side still pointing at a number that is about to stop existing.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	// Descending, from the top of the route down to the insertion point. An
	// ascending shift would collide with itself the moment it tried to move
	// checkpoint N into the slot checkpoint N+1 still occupies; descending
	// always moves into a slot that was just vacated, or was never occupied.
	for n := highest; n >= number; n-- {
		if _, err := tx.ExecContext(ctx,
			`UPDATE planning_checkpoints SET number = number + 1 WHERE project = ? AND number = ?`,
			project, n,
		); err != nil {
			return PlanningCheckpoint{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE planning_stones SET number = number + 1 WHERE project = ? AND number = ?`,
			project, n,
		); err != nil {
			return PlanningCheckpoint{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE planning_pebbles SET number = number + 1 WHERE project = ? AND number = ?`,
			project, n,
		); err != nil {
			return PlanningCheckpoint{}, unavailable(err)
		}
	}
	// One statement, not the descending loop above: sessions has no primary
	// key on (planning_project, planning_number), so nothing collides when
	// every matching row moves in the same UPDATE, and there is no highest
	// number to bound the shift at either.
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET planning_number = planning_number + 1 WHERE planning_project = ? AND planning_number >= ?`,
		project, number,
	); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	now := s.now().UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO planning_checkpoints(
			project, number, title, summary, status, body, answers, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'planned', '', '', ?, ?)
	`, project, number, title, summary, now, now); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE planning_projects SET route = ?, updated_at = ? WHERE project = ?`,
		route, now, project,
	); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	return s.PlanningCheckpoint(ctx, project, number)
}

// planningHighestCheckpointNumber is the top of the route so far, or 0 for a
// project with none. It is the bound an insert's number is checked against
// and the starting point the shift counts down from.
func planningHighestCheckpointNumber(
	ctx context.Context, q planningQuerier, project string,
) (int, error) {
	var highest sql.NullInt64
	if err := q.QueryRowContext(ctx,
		`SELECT MAX(number) FROM planning_checkpoints WHERE project = ?`, project,
	).Scan(&highest); err != nil {
		return 0, unavailable(err)
	}
	if !highest.Valid {
		return 0, nil
	}
	return int(highest.Int64), nil
}

// planningLowestBlockingCheckpoint is the smallest checkpoint number at or
// above number that is frozen or built, or 0 if the shift is clean. The
// lowest one is what the error names, because it is the first checkpoint the
// caller would need to deal with.
func planningLowestBlockingCheckpoint(
	ctx context.Context, q planningQuerier, project string, number int,
) (int, error) {
	var blocking sql.NullInt64
	if err := q.QueryRowContext(ctx, `
		SELECT MIN(number) FROM planning_checkpoints
		WHERE project = ? AND number >= ? AND status IN ('frozen', 'built')
	`, project, number).Scan(&blocking); err != nil {
		return 0, unavailable(err)
	}
	if !blocking.Valid {
		return 0, nil
	}
	return int(blocking.Int64), nil
}

// SavePlanningAnswers records what the human said at review. It is allowed at
// every status, including built: the answers are evidence of a conversation
// that already happened, and refusing to file them after a freeze would only
// mean the record of why something was frozen is missing from the one place
// that has the plan.
func (s *Store) SavePlanningAnswers(
	ctx context.Context, project string, number int, input SavePlanningAnswersRequest,
) (PlanningCheckpoint, error) {
	project, number, err := validPlanningCheckpointKey(project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	answers := strings.TrimSpace(input.Answers)
	if err := boundPlanningText(answers, planningMaxAnswersBytes, "invalid_planning_answers", "answers"); err != nil {
		return PlanningCheckpoint{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE planning_checkpoints SET answers = ?, updated_at = ?
		WHERE project = ? AND number = ?
	`, answers, s.now().UTC().UnixMilli(), project, number)
	if err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	} else if affected == 0 {
		return PlanningCheckpoint{}, ErrNotFound
	}
	return s.PlanningCheckpoint(ctx, project, number)
}

// TransitionPlanningCheckpoint moves a checkpoint along planningTransitions.
// The read and the write are one transaction so two clients racing a freeze
// cannot both see review and both succeed.
func (s *Store) TransitionPlanningCheckpoint(
	ctx context.Context, project string, number int, status string,
) (PlanningCheckpoint, error) {
	project, number, err := validPlanningCheckpointKey(project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if _, known := planningTransitions[status]; !known {
		return PlanningCheckpoint{}, invalid(
			"invalid_planning_status",
			"status must be one of: "+strings.Join(PlanningStatuses, ", "),
		)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	defer tx.Rollback()
	current, found, err := planningCheckpointStatus(ctx, tx, project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	if !found {
		return PlanningCheckpoint{}, ErrNotFound
	}
	if !planningTransitionAllowed(current, status) {
		return PlanningCheckpoint{}, conflict(
			"planning_transition_not_allowed",
			"a checkpoint cannot move from "+current+" to "+status,
		)
	}
	now := s.now().UTC().UnixMilli()
	// frozen_at is stamped once and never cleared, because there is no path
	// out of frozen. Built keeps the freeze time it already had: the question
	// frozen_at answers is when the text stopped changing, not when the work
	// finished.
	if status == "frozen" {
		_, err = tx.ExecContext(ctx, `
			UPDATE planning_checkpoints SET status = ?, frozen_at = ?, updated_at = ?
			WHERE project = ? AND number = ?
		`, status, now, now, project, number)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE planning_checkpoints SET status = ?, updated_at = ?
			WHERE project = ? AND number = ?
		`, status, now, project, number)
	}
	if err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	return s.PlanningCheckpoint(ctx, project, number)
}

func planningTransitionAllowed(from, to string) bool {
	for _, allowed := range planningTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ReplacePlanningPebbles writes a checkpoint's whole split as one batch: the
// stones, the pebbles, and which pebble sits in which stone.
//
// It refuses unless the checkpoint is frozen. Pebbles are pull requests, and
// cutting them from a plan that can still move is how four agents end up
// building against a spec that no longer says that. It is a replace and not a
// merge because a re-split is a new answer to "what is this made of", and
// merging one into the last would leave pebbles from an answer nobody gave.
func (s *Store) ReplacePlanningPebbles(
	ctx context.Context, project string, number int, input SavePlanningPebblesRequest,
) (PlanningCheckpoint, error) {
	project, number, err := validPlanningCheckpointKey(project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	stones, pebbles, err := normalizePlanningSplit(input)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	defer tx.Rollback()
	status, found, err := planningCheckpointStatus(ctx, tx, project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	if !found {
		return PlanningCheckpoint{}, ErrNotFound
	}
	if status != "frozen" {
		return PlanningCheckpoint{}, conflict(
			"planning_checkpoint_not_frozen",
			"pebbles can only be cut from a frozen checkpoint, and this one is "+status,
		)
	}
	// Pebbles first: planning_pebbles references planning_stones with a
	// cascading delete, so clearing the stones first would take the pebbles
	// with it and make the order load-bearing in a way a later reader would
	// have to reconstruct.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM planning_pebbles WHERE project = ? AND number = ?`, project, number,
	); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM planning_stones WHERE project = ? AND number = ?`, project, number,
	); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	now := s.now().UTC().UnixMilli()
	for _, stone := range stones {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO planning_stones(
				project, number, stone_id, ordinal, title, statement, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, project, number, stone.ID, stone.Ordinal, stone.Title, stone.Statement, now, now); err != nil {
			return PlanningCheckpoint{}, unavailable(err)
		}
	}
	for _, pebble := range pebbles {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO planning_pebbles(
				project, number, ordinal, slug, title, body, stone_id, work_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, project, number, pebble.Ordinal, pebble.Slug, pebble.Title, pebble.Body,
			nullablePlanningText(pebble.StoneID), nullablePlanningText(pebble.WorkID), now, now,
		); err != nil {
			return PlanningCheckpoint{}, unavailable(err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE planning_checkpoints SET updated_at = ? WHERE project = ? AND number = ?`,
		now, project, number,
	); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	return s.PlanningCheckpoint(ctx, project, number)
}

// normalizePlanningSplit validates a whole batch before any of it is written.
// Every failure names one field, because "invalid pebbles" against a batch of
// forty is not something a caller can act on.
func normalizePlanningSplit(
	input SavePlanningPebblesRequest,
) ([]PlanningStone, []PlanningPebble, error) {
	if len(input.Stones) > planningMaxStones {
		return nil, nil, invalid("invalid_planning_stones",
			"a checkpoint holds at most 50 stones")
	}
	if len(input.Pebbles) > planningMaxPebbles {
		return nil, nil, invalid("invalid_planning_pebbles",
			"a checkpoint holds at most 100 pebbles")
	}
	stones := make([]PlanningStone, 0, len(input.Stones))
	known := map[string]bool{}
	for index, stone := range input.Stones {
		id, err := validPlanningSlug(strings.ToLower(stone.ID), "invalid_planning_stone_id", "each stone id")
		if err != nil {
			return nil, nil, err
		}
		if known[id] {
			return nil, nil, invalid("duplicate_planning_stone_id",
				"stone id "+id+" appears twice in this checkpoint")
		}
		known[id] = true
		title := strings.TrimSpace(stone.Title)
		if err := boundPlanningText(title, planningMaxTitleBytes, "invalid_planning_stone_title", "each stone title"); err != nil {
			return nil, nil, err
		}
		statement := strings.TrimSpace(stone.Statement)
		if err := boundPlanningText(statement, planningMaxStatementBytes, "invalid_planning_stone_statement", "each stone statement"); err != nil {
			return nil, nil, err
		}
		// A stone with no title renders as an empty box on the page. Its id
		// is the name the human wrote, so it is the honest fallback.
		if title == "" {
			title = id
		}
		ordinal := stone.Ordinal
		if ordinal == 0 {
			ordinal = index + 1
		}
		if ordinal < 1 || ordinal > planningMaxStones {
			return nil, nil, invalid("invalid_planning_stone_ordinal",
				"each stone ordinal is between 1 and 50")
		}
		stones = append(stones, PlanningStone{
			ID: id, Ordinal: ordinal, Title: title, Statement: statement,
		})
	}
	pebbles := make([]PlanningPebble, 0, len(input.Pebbles))
	ordinals, slugs := map[int]bool{}, map[string]bool{}
	for index, pebble := range input.Pebbles {
		slug, err := validPlanningSlug(pebble.Slug, "invalid_planning_pebble_slug", "each pebble slug")
		if err != nil {
			return nil, nil, err
		}
		if len(slug) > planningMaxSlugBytes {
			return nil, nil, invalid("invalid_planning_pebble_slug",
				"each pebble slug is limited to 64 bytes")
		}
		if slugs[slug] {
			return nil, nil, invalid("duplicate_planning_pebble_slug",
				"pebble slug "+slug+" appears twice in this checkpoint")
		}
		slugs[slug] = true
		ordinal := pebble.Ordinal
		if ordinal == 0 {
			ordinal = index + 1
		}
		if ordinal < 1 || ordinal > planningMaxPebbles {
			return nil, nil, invalid("invalid_planning_pebble_ordinal",
				"each pebble ordinal is between 1 and 100")
		}
		if ordinals[ordinal] {
			return nil, nil, invalid("duplicate_planning_pebble_ordinal",
				"two pebbles claim the same ordinal in this checkpoint")
		}
		ordinals[ordinal] = true
		title := strings.TrimSpace(pebble.Title)
		if err := boundPlanningText(title, planningMaxTitleBytes, "invalid_planning_pebble_title", "each pebble title"); err != nil {
			return nil, nil, err
		}
		body := strings.TrimSpace(pebble.Body)
		if err := boundPlanningText(body, planningMaxPebbleBodyBytes, "invalid_planning_pebble_body", "each pebble body"); err != nil {
			return nil, nil, err
		}
		// The title is what the roadmap joins a pebble to its Work row by when
		// no work id has been recorded, so an untitled pebble would silently
		// stop being joinable. The slug is the only other name it has.
		if title == "" {
			title = slug
		}
		stoneID := strings.ToLower(strings.TrimSpace(pebble.StoneID))
		if stoneID != "" && !known[stoneID] {
			return nil, nil, invalid("unknown_planning_stone",
				"pebble "+slug+" names stone "+stoneID+", which is not in this batch")
		}
		workID := strings.TrimSpace(pebble.WorkID)
		if err := boundPlanningText(workID, planningMaxTitleBytes, "invalid_planning_work_id", "each pebble work id"); err != nil {
			return nil, nil, err
		}
		pebbles = append(pebbles, PlanningPebble{
			Ordinal: ordinal, Slug: slug, Title: title,
			Body: body, StoneID: stoneID, WorkID: workID,
		})
	}
	sort.SliceStable(stones, func(i, j int) bool { return stones[i].Ordinal < stones[j].Ordinal })
	sort.SliceStable(pebbles, func(i, j int) bool { return pebbles[i].Ordinal < pebbles[j].Ordinal })
	return stones, pebbles, nil
}

// MarkPlanningPebbleBuilt records that a pebble was built outside the
// factory: by hand, on a machine the factory does not reach, or by any other
// route that never became a Work row. It does not touch the freeze rules,
// because it records what happened to a pebble rather than changing the plan
// it was cut from, and it is allowed on any pebble regardless of its
// checkpoint's status.
//
// Sending it twice is allowed and the last ref wins, so a mistake in the ref
// is correctable without a second, different route.
func (s *Store) MarkPlanningPebbleBuilt(
	ctx context.Context, project string, number int, slug string, input MarkPlanningPebbleBuiltRequest,
) (PlanningPebble, error) {
	project, number, err := validPlanningCheckpointKey(project, number)
	if err != nil {
		return PlanningPebble{}, err
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	ref := strings.TrimSpace(input.Ref)
	if ref == "" {
		return PlanningPebble{}, invalid("invalid_planning_built_ref", "ref is required")
	}
	if err := boundPlanningText(ref, planningMaxBuiltRefBytes, "invalid_planning_built_ref", "ref"); err != nil {
		return PlanningPebble{}, err
	}
	now := s.now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `
		UPDATE planning_pebbles SET built_at = ?, built_ref = ?, updated_at = ?
		WHERE project = ? AND number = ? AND slug = ?
	`, now, ref, now, project, number, slug)
	if err != nil {
		return PlanningPebble{}, unavailable(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return PlanningPebble{}, unavailable(err)
	} else if affected == 0 {
		return PlanningPebble{}, ErrNotFound
	}
	return s.planningPebbleRow(ctx, project, number, slug)
}

// PlanningCheckpoint reads one checkpoint with its stones and pebbles.
func (s *Store) PlanningCheckpoint(
	ctx context.Context, project string, number int,
) (PlanningCheckpoint, error) {
	project, number, err := validPlanningCheckpointKey(project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	value, err := s.planningCheckpointRow(ctx, project, number)
	if err != nil {
		return PlanningCheckpoint{}, err
	}
	if value.Stones, err = s.planningStones(ctx, project, number); err != nil {
		return PlanningCheckpoint{}, err
	}
	if value.Pebbles, err = s.planningPebbles(ctx, project, number); err != nil {
		return PlanningCheckpoint{}, err
	}
	return value, nil
}

func (s *Store) planningCheckpointRow(
	ctx context.Context, project string, number int,
) (PlanningCheckpoint, error) {
	var value PlanningCheckpoint
	var created, updated int64
	var frozen sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT project, number, title, summary, status, body, answers, frozen_at, created_at, updated_at
		FROM planning_checkpoints WHERE project = ? AND number = ?
	`, project, number).Scan(
		&value.Project, &value.Number, &value.Title, &value.Summary, &value.Status,
		&value.Body, &value.Answers, &frozen, &created, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanningCheckpoint{}, ErrNotFound
	}
	if err != nil {
		return PlanningCheckpoint{}, unavailable(err)
	}
	value.CreatedAt = fromMillis(created)
	value.UpdatedAt = fromMillis(updated)
	if frozen.Valid {
		at := fromMillis(frozen.Int64)
		value.FrozenAt = &at
	}
	return value, nil
}

func (s *Store) planningStones(
	ctx context.Context, project string, number int,
) ([]PlanningStone, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stone_id, ordinal, title, statement FROM planning_stones
		WHERE project = ? AND number = ? ORDER BY ordinal, stone_id
	`, project, number)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	stones := []PlanningStone{}
	for rows.Next() {
		var stone PlanningStone
		if err := rows.Scan(&stone.ID, &stone.Ordinal, &stone.Title, &stone.Statement); err != nil {
			return nil, unavailable(err)
		}
		stones = append(stones, stone)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return stones, nil
}

func (s *Store) planningPebbles(
	ctx context.Context, project string, number int,
) ([]PlanningPebble, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ordinal, slug, title, body, COALESCE(stone_id, ''), COALESCE(work_id, ''), built_at, built_ref
		FROM planning_pebbles
		WHERE project = ? AND number = ? ORDER BY ordinal, slug
	`, project, number)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	pebbles := []PlanningPebble{}
	for rows.Next() {
		pebble, err := scanPlanningPebble(rows)
		if err != nil {
			return nil, unavailable(err)
		}
		pebbles = append(pebbles, pebble)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return pebbles, nil
}

// planningPebbleRow reads one pebble by its slug, the shape MarkPlanningPebbleBuilt
// returns so a caller sees exactly what it just saved without a second query
// of its own.
func (s *Store) planningPebbleRow(
	ctx context.Context, project string, number int, slug string,
) (PlanningPebble, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT ordinal, slug, title, body, COALESCE(stone_id, ''), COALESCE(work_id, ''), built_at, built_ref
		FROM planning_pebbles
		WHERE project = ? AND number = ? AND slug = ?
	`, project, number, slug)
	pebble, err := scanPlanningPebble(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanningPebble{}, ErrNotFound
	}
	if err != nil {
		return PlanningPebble{}, unavailable(err)
	}
	return pebble, nil
}

// planningPebbleScanner is the half of *sql.Row and *sql.Rows that
// scanPlanningPebble needs, so one scan reads both a single row and a batch.
type planningPebbleScanner interface {
	Scan(dest ...any) error
}

func scanPlanningPebble(row planningPebbleScanner) (PlanningPebble, error) {
	var pebble PlanningPebble
	var built sql.NullInt64
	var ref sql.NullString
	if err := row.Scan(
		&pebble.Ordinal, &pebble.Slug, &pebble.Title,
		&pebble.Body, &pebble.StoneID, &pebble.WorkID, &built, &ref,
	); err != nil {
		return PlanningPebble{}, err
	}
	if built.Valid {
		at := fromMillis(built.Int64)
		pebble.BuiltAt = &at
	}
	if ref.Valid {
		pebble.BuiltRef = ref.String
	}
	return pebble, nil
}

// planningQuerier is the half of *sql.Tx and *sql.DB the helpers below need,
// so a check can run inside the transaction that depends on it.
type planningQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func planningProjectExists(ctx context.Context, q planningQuerier, project string) error {
	var found int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM planning_projects WHERE project = ?`, project,
	).Scan(&found)
	if err != nil {
		return unavailable(err)
	}
	if found == 0 {
		// Named rather than a bare 404, because the caller's mistake is
		// almost always the order of two calls rather than a wrong project.
		return &ServiceError{
			Code:    "planning_project_not_found",
			Message: "save the project before saving one of its checkpoints",
			Status:  404,
		}
	}
	return nil
}

func planningCheckpointStatus(
	ctx context.Context, q planningQuerier, project string, number int,
) (string, bool, error) {
	var status string
	err := q.QueryRowContext(ctx,
		`SELECT status FROM planning_checkpoints WHERE project = ? AND number = ?`,
		project, number,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, unavailable(err)
	}
	return status, true, nil
}

func validPlanningCheckpointKey(project string, number int) (string, int, error) {
	project, err := validPlanningSlug(project, "invalid_planning_project", "project")
	if err != nil {
		return "", 0, err
	}
	if number < 1 || number > planningMaxCheckpointNumber {
		return "", 0, invalid("invalid_planning_number",
			"a checkpoint number is between 1 and 999")
	}
	return project, number, nil
}

func validPlanningSlug(value, code, field string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > planningMaxSlugBytes || !planningSlugPattern.MatchString(value) {
		return "", invalid(code, field+
			" must be lowercase letters, digits, and single dashes or underscores, up to 64 bytes")
	}
	return value, nil
}

// boundPlanningText caps a field by bytes rather than runes, matching the size
// bounding admission uses: the limit that matters is what a request body and a
// SQLite row actually hold, not how many characters a human counted.
func boundPlanningText(value string, limit int, code, field string) error {
	if len(value) > limit {
		return invalid(code, field+" exceeds its size limit")
	}
	return nil
}

func nullablePlanningText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
