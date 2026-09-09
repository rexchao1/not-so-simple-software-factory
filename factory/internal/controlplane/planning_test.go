package controlplane

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// seedPlanningProject saves a project and returns the store, so a test body
// starts at the first thing it actually cares about.
func seedPlanningProject(t *testing.T, store *Store, project string) PlanningProject {
	t.Helper()
	saved, err := store.UpsertPlanningProject(context.Background(), project, SavePlanningProjectRequest{
		Title:     "a new payer onboards itself",
		Statement: "Give it a new payer portal and it works, unattended.",
		Route:     "# Route: a new payer onboards itself\n",
	})
	if err != nil {
		t.Fatalf("save project: %v", err)
	}
	return saved
}

// seedPlanningCheckpoint writes a checkpoint with a body and walks it to the
// requested status along the only path that reaches it, so every test starts
// from a state the API itself could have produced.
func seedPlanningCheckpoint(
	t *testing.T, store *Store, project string, number int, status string,
) PlanningCheckpoint {
	t.Helper()
	ctx := context.Background()
	checkpoint, err := store.UpsertPlanningCheckpoint(ctx, project, number, SavePlanningCheckpointRequest{
		Title:   "The dashboard finishes a parked live payer",
		Summary: "the office fills a field into Settings",
		Body:    "# Checkpoint 2: The dashboard finishes a parked live payer\n\n## Slice\nA parked payer finishes.\n",
	})
	if err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	for _, step := range planningPathTo(t, status) {
		if checkpoint, err = store.TransitionPlanningCheckpoint(ctx, project, number, step); err != nil {
			t.Fatalf("transition to %s: %v", step, err)
		}
	}
	return checkpoint
}

// planningPathTo is the only route from planned to a given status. Tests use
// it rather than writing the status column directly, so a test can never set
// up a state the transition rules forbid.
func planningPathTo(t *testing.T, status string) []string {
	t.Helper()
	switch status {
	case "planned":
		return nil
	case "review":
		return []string{"review"}
	case "fog":
		return []string{"review", "fog"}
	case "frozen":
		return []string{"review", "frozen"}
	case "built":
		return []string{"review", "frozen", "built"}
	}
	t.Fatalf("no path to %q", status)
	return nil
}

func TestPlanningProjectRoundTripsAndCountsItsCheckpoints(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "planned")

	project, err := store.PlanningProject(ctx, "payer")
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if project.Title != "a new payer onboards itself" {
		t.Errorf("title = %q", project.Title)
	}
	if project.Statement == "" || !strings.HasPrefix(project.Statement, "Give it") {
		t.Errorf("statement = %q, want the boulder statement", project.Statement)
	}
	if project.Checkpoints != 2 {
		t.Errorf("checkpoint count = %d, want 2", project.Checkpoints)
	}
	if project.CreatedAt.IsZero() || project.UpdatedAt.IsZero() {
		t.Error("a saved project carries its timestamps")
	}
}

// A project saved twice is replaced, not duplicated. The light factory writes
// the route in one go and revises it in place.
func TestPlanningProjectUpsertReplacesRatherThanDuplicating(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	if _, err := store.UpsertPlanningProject(ctx, "payer", SavePlanningProjectRequest{
		Title: "a new payer onboards itself, revised",
		Route: "# Route: revised\n",
	}); err != nil {
		t.Fatalf("resave project: %v", err)
	}
	projects, err := store.PlanningProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("one project saved twice is one project, got %d", len(projects))
	}
	if projects[0].Title != "a new payer onboards itself, revised" {
		t.Errorf("title = %q, want the second save", projects[0].Title)
	}
	// A whole-resource PUT clears what it omits. A caller that meant to keep
	// the statement has to send it.
	if projects[0].Statement != "" {
		t.Errorf("statement = %q, want the omitted field cleared", projects[0].Statement)
	}
}

func TestPlanningProjectFallsBackToItsSlugForATitle(t *testing.T) {
	store := newTestStore(t)
	project, err := store.UpsertPlanningProject(
		context.Background(), "payer", SavePlanningProjectRequest{},
	)
	if err != nil {
		t.Fatalf("save project: %v", err)
	}
	if project.Title != "payer" {
		t.Errorf("title = %q, want the slug as the fallback", project.Title)
	}
}

func TestPlanningProjectsAreListedAlphabetically(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"zebra", "alpha", "middle"} {
		seedPlanningProject(t, store, name)
	}
	projects, err := store.PlanningProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	got := make([]string, 0, len(projects))
	for _, project := range projects {
		got = append(got, project.Project)
	}
	if strings.Join(got, ",") != "alpha,middle,zebra" {
		t.Errorf("order = %v, want a stable alphabetical list", got)
	}
}

func TestPlanningReadsOfWhatWasNeverSavedAreNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.PlanningProject(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project err = %v, want not found", err)
	}
	seedPlanningProject(t, store, "payer")
	if _, err := store.PlanningCheckpoint(ctx, "payer", 7); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing checkpoint err = %v, want not found", err)
	}
}

// A checkpoint belongs to a project. Saving one under a project that was never
// saved is almost always two calls in the wrong order, so the error says that
// rather than reporting a bare 404.
func TestPlanningCheckpointNeedsItsProjectFirst(t *testing.T) {
	store := newTestStore(t)
	_, err := store.UpsertPlanningCheckpoint(
		context.Background(), "payer", 1, SavePlanningCheckpointRequest{Title: "First"},
	)
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "planning_project_not_found" || service.Status != 404 {
		t.Fatalf("err = %#v, want 404 planning_project_not_found", err)
	}
}

func TestPlanningCheckpointStartsPlannedAndRoundTrips(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	saved := seedPlanningCheckpoint(t, store, "payer", 2, "planned")
	if saved.Status != "planned" {
		t.Errorf("status = %q, want planned", saved.Status)
	}
	if saved.FrozenAt != nil {
		t.Error("a planned checkpoint has no freeze time")
	}
	read, err := store.PlanningCheckpoint(ctx, "payer", 2)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if read.Title != saved.Title || read.Summary != saved.Summary || read.Body != saved.Body {
		t.Errorf("read = %+v, want the saved checkpoint", read)
	}
	if read.Stones == nil || read.Pebbles == nil {
		t.Error("an unsplit checkpoint reports empty lists, not nil")
	}
}

// The whole transition table, in both directions: every move it names must be
// taken, and every move it does not name must be refused with one code.
func TestPlanningEveryAllowedTransitionIsTakenAndEveryOtherIsRefused(t *testing.T) {
	allowed := map[string]bool{
		"planned>review": true,
		"review>fog":     true,
		"review>frozen":  true,
		"fog>frozen":     true,
		"frozen>built":   true,
	}
	ctx := context.Background()
	for _, from := range PlanningStatuses {
		for _, to := range PlanningStatuses {
			name := from + ">" + to
			t.Run(name, func(t *testing.T) {
				store := newTestStore(t)
				seedPlanningProject(t, store, "payer")
				seedPlanningCheckpoint(t, store, "payer", 1, from)
				after, err := store.TransitionPlanningCheckpoint(ctx, "payer", 1, to)
				if allowed[name] {
					if err != nil {
						t.Fatalf("%s must be allowed, got %v", name, err)
					}
					if after.Status != to {
						t.Errorf("status = %q, want %q", after.Status, to)
					}
					return
				}
				var service *ServiceError
				if !errors.As(err, &service) {
					t.Fatalf("%s must be refused, got %v", name, err)
				}
				if service.Code != "planning_transition_not_allowed" || service.Status != 409 {
					t.Fatalf("%s err = %#v, want 409 planning_transition_not_allowed", name, err)
				}
			})
		}
	}
}

// A status that is not one of the five is a caller error, not a refused move,
// so it is a 400 with its own code rather than the transition conflict.
func TestPlanningRefusesAStatusOutsideTheClosedSet(t *testing.T) {
	store := newTestStore(t)
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	for _, status := range []string{"drafting", "draft", "", "BUILT ONLY", "deleted"} {
		_, err := store.TransitionPlanningCheckpoint(context.Background(), "payer", 1, status)
		requireServiceError(t, err, "invalid_planning_status")
	}
}

// The status word is normalized before it is judged, so a caller that shouts
// or pads is not told its status does not exist.
func TestPlanningNormalizesTheStatusWord(t *testing.T) {
	store := newTestStore(t)
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	after, err := store.TransitionPlanningCheckpoint(context.Background(), "payer", 1, "  REVIEW ")
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if after.Status != "review" {
		t.Errorf("status = %q, want review", after.Status)
	}
}

func TestPlanningFreezingStampsTheMomentAndBuildingKeepsIt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	frozen := seedPlanningCheckpoint(t, store, "payer", 1, "frozen")
	if frozen.FrozenAt == nil || frozen.FrozenAt.IsZero() {
		t.Fatal("freezing stamps when the text stopped changing")
	}
	built, err := store.TransitionPlanningCheckpoint(ctx, "payer", 1, "built")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.FrozenAt == nil || !built.FrozenAt.Equal(*frozen.FrozenAt) {
		t.Errorf("frozen_at = %v, want the freeze time kept, not the build time", built.FrozenAt)
	}
}

// This is the promise the whole light-factory chain rests on: once a human has
// said freeze, the text the pebbles cite cannot be edited underneath them.
func TestPlanningFrozenAndBuiltCheckpointsRefuseBodyEdits(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"frozen", "built"} {
		t.Run(status, func(t *testing.T) {
			store := newTestStore(t)
			seedPlanningProject(t, store, "payer")
			before := seedPlanningCheckpoint(t, store, "payer", 1, status)
			_, err := store.UpsertPlanningCheckpoint(ctx, "payer", 1, SavePlanningCheckpointRequest{
				Title: "Something else", Body: "# rewritten\n",
			})
			var service *ServiceError
			if !errors.As(err, &service) || service.Code != "planning_checkpoint_frozen" || service.Status != 409 {
				t.Fatalf("err = %#v, want 409 planning_checkpoint_frozen", err)
			}
			after, err := store.PlanningCheckpoint(ctx, "payer", 1)
			if err != nil {
				t.Fatal(err)
			}
			if after.Body != before.Body || after.Title != before.Title {
				t.Errorf("a refused edit still changed the checkpoint: %+v", after)
			}
		})
	}
}

// Answers are the exception. They record what a human said, which stays true
// after a freeze, and the freeze itself is usually the reply to them.
func TestPlanningAnswersAreSavableAtEveryStatus(t *testing.T) {
	ctx := context.Background()
	for _, status := range PlanningStatuses {
		t.Run(status, func(t *testing.T) {
			store := newTestStore(t)
			seedPlanningProject(t, store, "payer")
			seedPlanningCheckpoint(t, store, "payer", 1, status)
			after, err := store.SavePlanningAnswers(ctx, "payer", 1, SavePlanningAnswersRequest{
				Answers: "Q1: rotate weekly. Q2: no.",
			})
			if err != nil {
				t.Fatalf("save answers on a %s checkpoint: %v", status, err)
			}
			if after.Answers != "Q1: rotate weekly. Q2: no." {
				t.Errorf("answers = %q", after.Answers)
			}
			if after.Status != status {
				t.Errorf("saving answers moved the status to %q", after.Status)
			}
		})
	}
}

func TestPlanningAnswersReplaceRatherThanAppend(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "review")
	if _, err := store.SavePlanningAnswers(ctx, "payer", 1, SavePlanningAnswersRequest{Answers: "first round"}); err != nil {
		t.Fatal(err)
	}
	after, err := store.SavePlanningAnswers(ctx, "payer", 1, SavePlanningAnswersRequest{Answers: "second round"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Answers != "second round" {
		t.Errorf("answers = %q, want the second save alone", after.Answers)
	}
}

func splitForTest() SavePlanningPebblesRequest {
	return SavePlanningPebblesRequest{
		Stones: []PlanningStone{
			{ID: "b1", Title: "Rebuild the driver", Statement: "The driver comes back."},
			{ID: "b2", Title: "Publish it", Statement: "The dashboard sees it."},
		},
		Pebbles: []PlanningPebble{
			{
				Slug: "live-driver", Title: "Rebuild a resumed live payer's own driver",
				StoneID: "b1",
				Body:    "## Rebuild a resumed live payer's own driver\n\n### What are we building?\nThe runner loses the driver\non a resume.\n",
			},
			{
				Slug: "publish", Title: "Let a live terminal run publish to the dashboard",
				StoneID: "b2", Body: "## Let a live terminal run publish to the dashboard\n",
			},
		},
	}
}

// A checkpoint that can still change must not be cut into pull requests.
func TestPlanningPebblesAreRefusedUnlessTheCheckpointIsFrozen(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"planned", "review", "fog"} {
		t.Run(status, func(t *testing.T) {
			store := newTestStore(t)
			seedPlanningProject(t, store, "payer")
			seedPlanningCheckpoint(t, store, "payer", 1, status)
			_, err := store.ReplacePlanningPebbles(ctx, "payer", 1, splitForTest())
			var service *ServiceError
			if !errors.As(err, &service) ||
				service.Code != "planning_checkpoint_not_frozen" || service.Status != 409 {
				t.Fatalf("err = %#v, want 409 planning_checkpoint_not_frozen", err)
			}
			after, err := store.PlanningCheckpoint(ctx, "payer", 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Pebbles) != 0 || len(after.Stones) != 0 {
				t.Errorf("a refused split still wrote rows: %+v", after)
			}
		})
	}
}

// Built is past frozen, and its pebbles are the ones that were built. Cutting
// a new set would rewrite the record of what actually shipped.
func TestPlanningPebblesAreRefusedOnABuiltCheckpoint(t *testing.T) {
	store := newTestStore(t)
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "built")
	_, err := store.ReplacePlanningPebbles(context.Background(), "payer", 1, splitForTest())
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "planning_checkpoint_not_frozen" {
		t.Fatalf("err = %#v, want planning_checkpoint_not_frozen", err)
	}
}

func TestPlanningPebblesSaveAsOneBatchWithTheirStones(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	after, err := store.ReplacePlanningPebbles(ctx, "payer", 2, splitForTest())
	if err != nil {
		t.Fatalf("save pebbles: %v", err)
	}
	if len(after.Stones) != 2 || len(after.Pebbles) != 2 {
		t.Fatalf("split = %d stones, %d pebbles", len(after.Stones), len(after.Pebbles))
	}
	// Ordinals are filled in from the order the batch arrived, so a caller
	// that sends an ordered list does not have to number it as well.
	if after.Pebbles[0].Ordinal != 1 || after.Pebbles[1].Ordinal != 2 {
		t.Errorf("ordinals = %d, %d", after.Pebbles[0].Ordinal, after.Pebbles[1].Ordinal)
	}
	if after.Pebbles[0].Slug != "live-driver" || after.Pebbles[0].StoneID != "b1" {
		t.Errorf("first pebble = %+v", after.Pebbles[0])
	}
	if after.Stones[0].Statement != "The driver comes back." {
		t.Errorf("stone statement = %q", after.Stones[0].Statement)
	}
}

// A re-split is a new answer to "what is this made of". Merging it into the
// last one would leave pebbles nobody asked for a second time.
func TestPlanningPebblesReplaceTheWholePreviousSplit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	if _, err := store.ReplacePlanningPebbles(ctx, "payer", 2, splitForTest()); err != nil {
		t.Fatal(err)
	}
	after, err := store.ReplacePlanningPebbles(ctx, "payer", 2, SavePlanningPebblesRequest{
		Stones:  []PlanningStone{{ID: "b9", Title: "One stone now"}},
		Pebbles: []PlanningPebble{{Slug: "only-task", Title: "The only task", StoneID: "b9"}},
	})
	if err != nil {
		t.Fatalf("resplit: %v", err)
	}
	if len(after.Pebbles) != 1 || after.Pebbles[0].Slug != "only-task" {
		t.Errorf("pebbles = %+v, want only the second split", after.Pebbles)
	}
	if len(after.Stones) != 1 || after.Stones[0].ID != "b9" {
		t.Errorf("stones = %+v, want only the second split", after.Stones)
	}
}

func TestPlanningPebblesMayCarryTheWorkTheyBecame(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	split := splitForTest()
	split.Pebbles[0].WorkID = "work-123"
	after, err := store.ReplacePlanningPebbles(ctx, "payer", 2, split)
	if err != nil {
		t.Fatalf("save pebbles: %v", err)
	}
	if after.Pebbles[0].WorkID != "work-123" {
		t.Errorf("work id = %q", after.Pebbles[0].WorkID)
	}
	// A pebble that has not been submitted has no work id, and the column is
	// NULL rather than an empty string, so a later join cannot match on "".
	if after.Pebbles[1].WorkID != "" {
		t.Errorf("unsubmitted pebble work id = %q, want empty", after.Pebbles[1].WorkID)
	}
	var nulls int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM planning_pebbles WHERE work_id IS NULL`,
	).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Errorf("null work ids = %d, want 1", nulls)
	}
}

// A pebble may name no stone at all. The page puts those in a catch-all
// rather than hiding them, so the batch must not insist on a grouping.
func TestPlanningPebblesMayBeUngrouped(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	after, err := store.ReplacePlanningPebbles(ctx, "payer", 2, SavePlanningPebblesRequest{
		Pebbles: []PlanningPebble{{Slug: "loose", Title: "A loose task"}},
	})
	if err != nil {
		t.Fatalf("save ungrouped pebbles: %v", err)
	}
	if len(after.Pebbles) != 1 || after.Pebbles[0].StoneID != "" {
		t.Errorf("pebbles = %+v", after.Pebbles)
	}
}

// A pebble pointing at a stone the batch does not contain would be a dangling
// group, so the whole batch is refused and named.
func TestPlanningPebblesRefuseAnUnknownStone(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	_, err := store.ReplacePlanningPebbles(ctx, "payer", 2, SavePlanningPebblesRequest{
		Stones:  []PlanningStone{{ID: "b1", Title: "Only stone"}},
		Pebbles: []PlanningPebble{{Slug: "orphan", Title: "Orphan", StoneID: "b7"}},
	})
	requireServiceError(t, err, "unknown_planning_stone")
	if !strings.Contains(err.Error(), "orphan") {
		t.Errorf("the error names the pebble that is wrong: %v", err)
	}
}

// A pebble built by hand, outside the factory, is recorded through its own
// route rather than a re-split.
func TestPlanningPebbleCanBeMarkedBuiltByHand(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	if _, err := store.ReplacePlanningPebbles(ctx, "payer", 2, splitForTest()); err != nil {
		t.Fatal(err)
	}
	pebble, err := store.MarkPlanningPebbleBuilt(ctx, "payer", 2, "live-driver",
		MarkPlanningPebbleBuiltRequest{Ref: "a1b2c3d4"})
	if err != nil {
		t.Fatalf("mark built: %v", err)
	}
	if pebble.BuiltAt == nil || pebble.BuiltAt.IsZero() {
		t.Errorf("built_at = %v, want it set", pebble.BuiltAt)
	}
	if pebble.BuiltRef != "a1b2c3d4" {
		t.Errorf("built_ref = %q", pebble.BuiltRef)
	}
	after, err := store.PlanningCheckpoint(ctx, "payer", 2)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pebbles[0].BuiltAt == nil || after.Pebbles[0].BuiltRef != "a1b2c3d4" {
		t.Errorf("checkpoint pebble = %+v, want the built columns to round trip", after.Pebbles[0])
	}
}

// A blank ref is refused: there is nothing to point at, so nothing to record.
func TestPlanningPebbleBuiltRefusesABlankRef(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	if _, err := store.ReplacePlanningPebbles(ctx, "payer", 2, splitForTest()); err != nil {
		t.Fatal(err)
	}
	_, err := store.MarkPlanningPebbleBuilt(ctx, "payer", 2, "live-driver",
		MarkPlanningPebbleBuiltRequest{Ref: "   "})
	requireServiceError(t, err, "invalid_planning_built_ref")
}

// An unknown project, checkpoint or slug is a 404: there is nothing there to
// mark.
func TestPlanningPebbleBuiltRefusesAnUnknownSlug(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	if _, err := store.ReplacePlanningPebbles(ctx, "payer", 2, splitForTest()); err != nil {
		t.Fatal(err)
	}
	_, err := store.MarkPlanningPebbleBuilt(ctx, "payer", 2, "no-such-pebble",
		MarkPlanningPebbleBuiltRequest{Ref: "a1b2c3d4"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %#v, want ErrNotFound", err)
	}
	_, err = store.MarkPlanningPebbleBuilt(ctx, "payer", 9, "live-driver",
		MarkPlanningPebbleBuiltRequest{Ref: "a1b2c3d4"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown checkpoint: err = %#v, want ErrNotFound", err)
	}
	_, err = store.MarkPlanningPebbleBuilt(ctx, "no-such-project", 2, "live-driver",
		MarkPlanningPebbleBuiltRequest{Ref: "a1b2c3d4"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: err = %#v, want ErrNotFound", err)
	}
}

// Sending it twice is a mistake being corrected, not an error: the last ref
// wins.
func TestPlanningPebbleBuiltASecondTimeOverwritesTheFirst(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	if _, err := store.ReplacePlanningPebbles(ctx, "payer", 2, splitForTest()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkPlanningPebbleBuilt(ctx, "payer", 2, "live-driver",
		MarkPlanningPebbleBuiltRequest{Ref: "first-sha"}); err != nil {
		t.Fatal(err)
	}
	pebble, err := store.MarkPlanningPebbleBuilt(ctx, "payer", 2, "live-driver",
		MarkPlanningPebbleBuiltRequest{Ref: "https://example.test/pr/9"})
	if err != nil {
		t.Fatal(err)
	}
	if pebble.BuiltRef != "https://example.test/pr/9" {
		t.Errorf("built_ref = %q, want the second call to win", pebble.BuiltRef)
	}
}

func TestPlanningSplitRefusesDuplicateNamesAndOrdinals(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		input SavePlanningPebblesRequest
		code  string
	}{
		{
			"two stones sharing an id",
			SavePlanningPebblesRequest{Stones: []PlanningStone{{ID: "b1"}, {ID: "b1"}}},
			"duplicate_planning_stone_id",
		},
		{
			"two pebbles sharing a slug",
			SavePlanningPebblesRequest{Pebbles: []PlanningPebble{
				{Slug: "same", Ordinal: 1}, {Slug: "same", Ordinal: 2},
			}},
			"duplicate_planning_pebble_slug",
		},
		{
			"two pebbles sharing an ordinal",
			SavePlanningPebblesRequest{Pebbles: []PlanningPebble{
				{Slug: "one", Ordinal: 3}, {Slug: "two", Ordinal: 3},
			}},
			"duplicate_planning_pebble_ordinal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			seedPlanningProject(t, store, "payer")
			seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
			_, err := store.ReplacePlanningPebbles(ctx, "payer", 2, tc.input)
			requireServiceError(t, err, tc.code)
		})
	}
}

// Every text field is bounded, and every bound names the field it belongs to.
// An unbounded body is a way to put a megabyte into a page that renders it.
func TestPlanningBoundsEveryTextField(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	long := func(n int) string { return strings.Repeat("x", n+1) }

	requireServiceError(t, firstProjectError(store.UpsertPlanningProject(ctx, "payer",
		SavePlanningProjectRequest{Title: long(planningMaxTitleBytes)})), "invalid_planning_title")
	requireServiceError(t, firstProjectError(store.UpsertPlanningProject(ctx, "payer",
		SavePlanningProjectRequest{Statement: long(planningMaxStatementBytes)})), "invalid_planning_statement")
	requireServiceError(t, firstProjectError(store.UpsertPlanningProject(ctx, "payer",
		SavePlanningProjectRequest{Route: long(planningMaxRouteBytes)})), "invalid_planning_route")

	requireServiceError(t, firstCheckpointError(store.UpsertPlanningCheckpoint(ctx, "payer", 3,
		SavePlanningCheckpointRequest{Title: long(planningMaxTitleBytes)})), "invalid_planning_title")
	requireServiceError(t, firstCheckpointError(store.UpsertPlanningCheckpoint(ctx, "payer", 3,
		SavePlanningCheckpointRequest{Summary: long(planningMaxSummaryBytes)})), "invalid_planning_summary")
	requireServiceError(t, firstCheckpointError(store.UpsertPlanningCheckpoint(ctx, "payer", 3,
		SavePlanningCheckpointRequest{Body: long(planningMaxBodyBytes)})), "invalid_planning_body")
	requireServiceError(t, firstCheckpointError(store.SavePlanningAnswers(ctx, "payer", 2,
		SavePlanningAnswersRequest{Answers: long(planningMaxAnswersBytes)})), "invalid_planning_answers")

	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Stones: []PlanningStone{{ID: "b1", Title: long(planningMaxTitleBytes)}}},
	)), "invalid_planning_stone_title")
	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Stones: []PlanningStone{{ID: "b1", Statement: long(planningMaxStatementBytes)}}},
	)), "invalid_planning_stone_statement")
	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Pebbles: []PlanningPebble{{Slug: "one", Title: long(planningMaxTitleBytes)}}},
	)), "invalid_planning_pebble_title")
	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Pebbles: []PlanningPebble{{Slug: "one", Body: long(planningMaxPebbleBodyBytes)}}},
	)), "invalid_planning_pebble_body")
}

func TestPlanningBoundsHowManyPebblesAndStonesOneCheckpointHolds(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")

	stones := make([]PlanningStone, planningMaxStones+1)
	for i := range stones {
		stones[i] = PlanningStone{ID: "b" + strconv.Itoa(i+1)}
	}
	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Stones: stones})), "invalid_planning_stones")

	pebbles := make([]PlanningPebble, planningMaxPebbles+1)
	for i := range pebbles {
		pebbles[i] = PlanningPebble{Slug: "p" + strconv.Itoa(i+1)}
	}
	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Pebbles: pebbles})), "invalid_planning_pebbles")
}

// Slugs reach a URL path, a worktree filename, and a human's eye, so the shape
// is narrow on purpose and every way of missing it is refused.
func TestPlanningValidatesSlugsAndNumbers(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, bad := range []string{
		"", "  ", "-leading", "trailing-", "two--dashes", "has space",
		"has/slash", "has.dot", "Ünicode", strings.Repeat("a", 65),
	} {
		_, err := store.UpsertPlanningProject(ctx, bad, SavePlanningProjectRequest{})
		requireServiceError(t, err, "invalid_planning_project")
	}
	// Uppercase is folded rather than refused: the same project named two ways
	// is one project, and refusing would be a rule with no purpose behind it.
	if _, err := store.UpsertPlanningProject(ctx, " Payer ", SavePlanningProjectRequest{}); err != nil {
		t.Fatalf("a padded, capitalised slug is normalized, got %v", err)
	}
	if _, err := store.PlanningProject(ctx, "payer"); err != nil {
		t.Fatalf("the normalized project is readable by its slug: %v", err)
	}

	seedPlanningProject(t, store, "payer")
	for _, number := range []int{0, -1, 1000, 100000} {
		_, err := store.UpsertPlanningCheckpoint(ctx, "payer", number, SavePlanningCheckpointRequest{})
		requireServiceError(t, err, "invalid_planning_number")
	}
}

func TestPlanningValidatesPebbleAndStoneIdentifiers(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Stones: []PlanningStone{{ID: "not a slug"}}},
	)), "invalid_planning_stone_id")
	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Pebbles: []PlanningPebble{{Slug: "not a slug"}}},
	)), "invalid_planning_pebble_slug")
	requireServiceError(t, firstCheckpointError(store.ReplacePlanningPebbles(ctx, "payer", 2,
		SavePlanningPebblesRequest{Pebbles: []PlanningPebble{{Slug: "fine", Ordinal: 101}}},
	)), "invalid_planning_pebble_ordinal")
}

// The store validates first, but the column constrains too, so a future writer
// that skips the store cannot put a sixth status in the table.
func TestPlanningStatusColumnRefusesAnythingOutsideTheClosedSet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	_, err := store.db.ExecContext(ctx,
		`UPDATE planning_checkpoints SET status = 'drafting' WHERE project = 'payer'`)
	if err == nil {
		t.Fatal("the CHECK constraint accepted a status outside the closed set")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("err = %v, want a constraint failure", err)
	}
}

func firstProjectError(_ PlanningProject, err error) error       { return err }
func firstCheckpointError(_ PlanningCheckpoint, err error) error { return err }

// A clean insert in the middle renumbers everything above it. Three
// consecutive checkpoints, so the descending shift has to move more than one
// row without colliding with itself on the (project, number) primary key.
//
// The checkpoint carrying the pebbles below is written directly rather than
// through ReplacePlanningPebbles, because a checkpoint that holds pebbles
// through the ordinary API is always frozen or built, and both of those
// statuses would themselves block this shift; the direct write isolates the
// renumbering SQL's own correctness (stones and pebbles keyed on number move
// with their checkpoint) from that separate, already-covered refusal.
func TestPlanningInsertShiftsTheUnbuiltTailUp(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "planned")
	seedPlanningCheckpoint(t, store, "payer", 3, "planned")
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO planning_stones(project, number, stone_id, ordinal, title, statement, created_at, updated_at)
		VALUES ('payer', 2, 'b1', 1, 'Rebuild the driver', '', 0, 0)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO planning_pebbles(project, number, ordinal, slug, title, body, stone_id, work_id, created_at, updated_at)
		VALUES ('payer', 2, 1, 'live-driver', 'Rebuild the driver', '', 'b1', NULL, 0, 0)
	`); err != nil {
		t.Fatal(err)
	}

	inserted, err := store.InsertPlanningCheckpoint(ctx, "payer", 2, InsertPlanningCheckpointRequest{
		Title:   "A checkpoint that turned up mid-route",
		Summary: "found late, built in turn",
		Route:   "# Route: revised with the insert\n",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if inserted.Number != 2 || inserted.Status != "planned" {
		t.Fatalf("inserted = %+v", inserted)
	}

	one, err := store.PlanningCheckpoint(ctx, "payer", 1)
	if err != nil {
		t.Fatal(err)
	}
	if one.Title == inserted.Title {
		t.Error("checkpoint 1 must be untouched by an insert above it")
	}

	shiftedTwoToThree, err := store.PlanningCheckpoint(ctx, "payer", 3)
	if err != nil {
		t.Fatalf("read shifted checkpoint: %v", err)
	}
	if len(shiftedTwoToThree.Pebbles) != 1 || len(shiftedTwoToThree.Stones) != 1 {
		t.Fatalf("shifted checkpoint's split = %+v", shiftedTwoToThree)
	}
	if shiftedTwoToThree.Pebbles[0].Slug != "live-driver" || shiftedTwoToThree.Stones[0].ID != "b1" {
		t.Errorf("shifted checkpoint = %+v, want the stone and pebble carried up with it", shiftedTwoToThree)
	}

	shiftedThreeToFour, err := store.PlanningCheckpoint(ctx, "payer", 4)
	if err != nil {
		t.Fatalf("read shifted checkpoint: %v", err)
	}
	if shiftedThreeToFour.Number != 4 {
		t.Errorf("number = %d, want 4", shiftedThreeToFour.Number)
	}

	project, err := store.PlanningProject(ctx, "payer")
	if err != nil {
		t.Fatal(err)
	}
	if project.Route != "# Route: revised with the insert" {
		t.Errorf("route = %q, want the request's route", project.Route)
	}
	if project.Checkpoints != 4 {
		t.Errorf("checkpoint count = %d, want 4", project.Checkpoints)
	}
}

// Inserting past the end of the route, at highest+1, is just an append: no
// shift is needed and none happens.
func TestPlanningInsertAtTheEndAppendsWithoutShifting(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "frozen")

	inserted, err := store.InsertPlanningCheckpoint(ctx, "payer", 2, InsertPlanningCheckpointRequest{
		Title: "The next rung", Route: "# Route\n",
	})
	if err != nil {
		t.Fatalf("insert at the end: %v", err)
	}
	if inserted.Number != 2 {
		t.Errorf("number = %d, want 2", inserted.Number)
	}
	one, err := store.PlanningCheckpoint(ctx, "payer", 1)
	if err != nil {
		t.Fatal(err)
	}
	if one.Status != "frozen" {
		t.Errorf("an append past the end must not touch what is already there: status = %q", one.Status)
	}
}

// A frozen checkpoint above the insertion point blocks the whole shift, and
// the error names it.
func TestPlanningInsertRefusedByAFrozenCheckpointAbove(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")

	before, err := store.PlanningProject(ctx, "payer")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.InsertPlanningCheckpoint(ctx, "payer", 1, InsertPlanningCheckpointRequest{
		Title: "Should not land", Route: "# Rewritten route\n",
	})
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "planning_insert_not_clean" || service.Status != 409 {
		t.Fatalf("err = %#v, want 409 planning_insert_not_clean", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("the error names the lowest blocking number: %v", err)
	}
	after, err := store.PlanningProject(ctx, "payer")
	if err != nil {
		t.Fatal(err)
	}
	if after.Route != before.Route {
		t.Errorf("a refused insert must not touch the route: %q", after.Route)
	}
	if after.Checkpoints != before.Checkpoints {
		t.Errorf("a refused insert must not add a checkpoint")
	}
}

// A built checkpoint above the insertion point blocks it exactly like a
// frozen one: freezing and building are the same promise not to move.
func TestPlanningInsertRefusedByABuiltCheckpointAbove(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "built")

	_, err := store.InsertPlanningCheckpoint(ctx, "payer", 2, InsertPlanningCheckpointRequest{
		Title: "Should not land", Route: "# Rewritten route\n",
	})
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "planning_insert_not_clean" || service.Status != 409 {
		t.Fatalf("err = %#v, want 409 planning_insert_not_clean", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("the error names the lowest blocking number: %v", err)
	}
	built, err := store.PlanningCheckpoint(ctx, "payer", 2)
	if err != nil {
		t.Fatal(err)
	}
	if built.Status != "built" {
		t.Errorf("a refused insert must not touch the checkpoint it was blocked by: status = %q", built.Status)
	}
}

// A number outside 1 through the highest existing checkpoint plus one is a
// caller error, not a conflict, and it leaves nothing written.
func TestPlanningInsertRefusesANumberOutOfRange(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "planned")

	before, err := store.PlanningProject(ctx, "payer")
	if err != nil {
		t.Fatal(err)
	}
	for _, number := range []int{0, -1, 4, 1000} {
		_, err := store.InsertPlanningCheckpoint(ctx, "payer", number, InsertPlanningCheckpointRequest{
			Title: "Out of range", Route: "# Rewritten\n",
		})
		requireServiceError(t, err, "planning_invalid_checkpoint_number")
	}
	after, err := store.PlanningProject(ctx, "payer")
	if err != nil {
		t.Fatal(err)
	}
	if after.Route != before.Route || after.Checkpoints != before.Checkpoints {
		t.Errorf("a refused insert must not touch the project: before %+v, after %+v", before, after)
	}
}

// An insert into a project that does not exist fails the same way any other
// checkpoint write does: the project has to be saved first.
func TestPlanningInsertNeedsItsProjectFirst(t *testing.T) {
	store := newTestStore(t)
	_, err := store.InsertPlanningCheckpoint(context.Background(), "payer", 1, InsertPlanningCheckpointRequest{
		Title: "First", Route: "# Route\n",
	})
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "planning_project_not_found" || service.Status != 404 {
		t.Fatalf("err = %#v, want 404 planning_project_not_found", err)
	}
}

// admitPlanningPassForTest admits one critique Work item against project,
// number and round and returns its id, so a test can build up sessions rows
// carrying the planning triple without running a critique end to end.
func admitPlanningPassForTest(
	t *testing.T, store *Store, requestKey string, number, round int,
) string {
	t.Helper()
	response, err := admitPlanningWorkForTest(t, store, admitPlanningOptions{
		RequestKey: requestKey,
		Planning:   &protocol.WorkPlanning{Project: "payer", Number: number, Round: round},
	})
	if err != nil {
		t.Fatalf("admit a planning pass: %v", err)
	}
	return response.WorkIDs[0]
}

// A checkpoint in review is exactly the kind that has critique rounds behind
// it, and an insert below it must carry those rounds up with it rather than
// leaving them on the number the checkpoint no longer has.
func TestPlanningInsertShiftsCritiquePassHistory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "review")
	admitPlanningPassForTest(t, store, "55000000-0000-4000-8000-000000000001", 2, 1)
	admitPlanningPassForTest(t, store, "55000000-0000-4000-8000-000000000002", 2, 2)

	if _, err := store.InsertPlanningCheckpoint(ctx, "payer", 2, InsertPlanningCheckpointRequest{
		Title: "A checkpoint that turned up mid-route", Route: "# Route: revised\n",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	shifted := roadmapFixtureCheckpoint(t, store, 3)
	if shifted.PassRounds != 2 || len(shifted.Passes) != 2 {
		t.Errorf("shifted checkpoint = %+v, want its two rounds carried up with it", shifted)
	}
	inserted := roadmapFixtureCheckpoint(t, store, 2)
	if inserted.PassRounds != 0 || len(inserted.Passes) != 0 {
		t.Errorf("inserted checkpoint = %+v, want no history of its own", inserted)
	}
}

// Three consecutive checkpoints, each carrying a round, so the set-based
// shift has to move every one of them exactly once: an off-by-one bound would
// either double up a round on one checkpoint or leave one behind on its old
// number.
func TestPlanningInsertShiftsCritiquePassHistoryAcrossConsecutiveCheckpoints(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "review")
	seedPlanningCheckpoint(t, store, "payer", 3, "review")
	seedPlanningCheckpoint(t, store, "payer", 4, "review")
	admitPlanningPassForTest(t, store, "55000000-0000-4000-8000-000000000003", 2, 1)
	admitPlanningPassForTest(t, store, "55000000-0000-4000-8000-000000000004", 3, 1)
	admitPlanningPassForTest(t, store, "55000000-0000-4000-8000-000000000005", 4, 1)

	if _, err := store.InsertPlanningCheckpoint(ctx, "payer", 2, InsertPlanningCheckpointRequest{
		Title: "Squeezed in ahead of three passed checkpoints", Route: "# Route: revised\n",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	inserted := roadmapFixtureCheckpoint(t, store, 2)
	if inserted.PassRounds != 0 {
		t.Errorf("inserted checkpoint = %+v, want no history", inserted)
	}
	for old, want := range map[int]int{2: 3, 3: 4, 4: 5} {
		checkpoint := roadmapFixtureCheckpoint(t, store, want)
		if checkpoint.PassRounds != 1 {
			t.Errorf("checkpoint that was %d, now %d = %+v, want exactly one round", old, want, checkpoint)
		}
	}
}

// A sessions row can carry a planning_number above every checkpoint that
// currently exists, the way a critiqued checkpoint that was later deleted
// would leave one behind. The shift is a plain comparison against number, not
// a join against planning_checkpoints, so a row like this has to move too
// rather than being silently skipped as out of range.
func TestPlanningInsertShiftsAPassAboveTheHighestCheckpoint(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "planned")
	workID := admitPlanningPassForTest(t, store, "55000000-0000-4000-8000-000000000006", 5, 1)

	if _, err := store.InsertPlanningCheckpoint(ctx, "payer", 1, InsertPlanningCheckpointRequest{
		Title: "Ahead of everything", Route: "# Route: revised\n",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, number, _ := storedPlanningTriple(t, store, workID)
	if number != 6 {
		t.Errorf("planning_number = %d, want 6, shifted past the highest checkpoint too", number)
	}
}

// A refused insert returns before any UPDATE runs, so this is a guard on the
// early return rather than a proof that a partial shift was rolled back.
func TestPlanningInsertRefusalLeavesCritiquePassHistoryUntouched(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	seedPlanningCheckpoint(t, store, "payer", 2, "frozen")
	workID := admitPlanningPassForTest(t, store, "55000000-0000-4000-8000-000000000007", 2, 1)

	_, err := store.InsertPlanningCheckpoint(ctx, "payer", 1, InsertPlanningCheckpointRequest{
		Title: "Should not land", Route: "# Rewritten route\n",
	})
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "planning_insert_not_clean" {
		t.Fatalf("err = %#v, want planning_insert_not_clean", err)
	}
	project, number, round := storedPlanningTriple(t, store, workID)
	if project != "payer" || number != 2 || round != 1 {
		t.Errorf("triple = %q %d %d, want unchanged payer 2 1", project, number, round)
	}
}
