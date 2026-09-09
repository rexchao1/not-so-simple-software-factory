package controlplane

import (
	"context"
	"strings"
	"testing"
)

// planningTables is every table migration 046 creates, under the names they
// carry once 048 has renamed the middle one from planning_boulders.
var planningTables = []string{
	"planning_projects", "planning_checkpoints", "planning_stones", "planning_pebbles",
}

// planningRenameVersions are the two migrations that own those tables: 046
// creates them and 048 renames one. A test that rolls the planning schema back
// has to roll back both, because replaying 046 alone would leave the boulder
// names the store no longer queries.
var planningRenameVersions = []int{46, 48}

func TestMigration046CreatesThePlanningTablesOnAFreshDatabase(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, table := range planningTables {
		var count int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("%s was not created", table)
		}
	}
}

// The five statuses are the closed set. They are checked in the DDL as well as
// in the store, because the column is the last line of defence against a
// writer that skips the store.
func TestMigration046ConstrainsTheCheckpointStatus(t *testing.T) {
	store := newTestStore(t)
	var ddl string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'planning_checkpoints'`,
	).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	for _, status := range PlanningStatuses {
		if !strings.Contains(ddl, "'"+status+"'") {
			t.Errorf("the status CHECK does not admit %q:\n%s", status, ddl)
		}
	}
	// The orchestrator's own "draft" status is deliberately not in the set.
	// A checkpoint being written in the agent's context is indistinguishable
	// from planned until the draft is saved and offered for review.
	if strings.Contains(ddl, "'draft'") || strings.Contains(ddl, "'drafting'") {
		t.Errorf("the status CHECK still admits a drafting status:\n%s", ddl)
	}
}

// Each level's identity is unique inside its parent, which is what lets the
// API address a checkpoint as (project, number) with no surrogate id.
func TestMigration046MakesEachIdentityUniqueInsideItsParent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "frozen")
	if _, err := store.ReplacePlanningPebbles(ctx, "payer", 1, SavePlanningPebblesRequest{
		Stones:  []PlanningStone{{ID: "b1", Title: "One"}},
		Pebbles: []PlanningPebble{{Slug: "first", Title: "First", StoneID: "b1"}},
	}); err != nil {
		t.Fatal(err)
	}
	duplicates := []struct {
		name      string
		statement string
		args      []any
	}{
		{
			"a second project row",
			`INSERT INTO planning_projects(project, title, statement, route, created_at, updated_at)
			 VALUES ('payer', '', '', '', 0, 0)`, nil,
		},
		{
			"a second checkpoint 1",
			`INSERT INTO planning_checkpoints(project, number, created_at, updated_at)
			 VALUES ('payer', 1, 0, 0)`, nil,
		},
		{
			"a second stone b1",
			`INSERT INTO planning_stones(project, number, stone_id, ordinal, created_at, updated_at)
			 VALUES ('payer', 1, 'b1', 2, 0, 0)`, nil,
		},
		{
			"a second pebble at ordinal 1",
			`INSERT INTO planning_pebbles(project, number, ordinal, slug, created_at, updated_at)
			 VALUES ('payer', 1, 1, 'other', 0, 0)`, nil,
		},
		{
			"a second pebble with the slug first",
			`INSERT INTO planning_pebbles(project, number, ordinal, slug, created_at, updated_at)
			 VALUES ('payer', 1, 2, 'first', 0, 0)`, nil,
		},
	}
	for _, duplicate := range duplicates {
		t.Run(duplicate.name, func(t *testing.T) {
			if _, err := store.db.ExecContext(ctx, duplicate.statement, duplicate.args...); err == nil {
				t.Fatal("the database accepted a duplicate identity")
			}
		})
	}
}

// A checkpoint under no project, or a stone under no checkpoint, is a row
// nothing can render. The foreign keys refuse both.
func TestMigration046RefusesOrphansAndCascadesDeletes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO planning_checkpoints(project, number, created_at, updated_at)
		 VALUES ('nothing', 1, 0, 0)`); err == nil {
		t.Fatal("a checkpoint under no project was accepted")
	}
	seedPlanningProject(t, store, "payer")
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO planning_stones(project, number, stone_id, ordinal, created_at, updated_at)
		 VALUES ('payer', 99, 'b1', 1, 0, 0)`); err == nil {
		t.Fatal("a stone under no checkpoint was accepted")
	}
	seedPlanningCheckpoint(t, store, "payer", 1, "frozen")
	if _, err := store.ReplacePlanningPebbles(ctx, "payer", 1, SavePlanningPebblesRequest{
		Stones:  []PlanningStone{{ID: "b1"}},
		Pebbles: []PlanningPebble{{Slug: "first", StoneID: "b1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM planning_projects WHERE project = 'payer'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range planningTables[1:] {
		var remaining int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Errorf("%s kept %d rows after its project was deleted", table, remaining)
		}
	}
}

// 046 has to land on a database that already holds real rows from 001 through
// 045, not only on an empty one.
//
// The rollback below is exact rather than approximate: 046 creates four tables
// and their indexes and 048 renames one of them, and between them they touch
// nothing else, so a database with those objects dropped and both ledger rows
// removed is a database at 045 in every way that matters to these two
// migrations. Building one by replaying 45 files instead would mean a second
// copy of the migration loop in the test, which is the thing most likely to
// drift away from the one that runs in production.
//
// 047 is deliberately left applied. It adds columns to tables these two never
// touch, and replaying an ADD COLUMN onto a column that is already there would
// fail for a reason that has nothing to do with what is being tested here.
func TestMigration046AppliesToADatabaseAlreadyAt045(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Real rows from the earlier migrations, so 046 is not applying to a
	// schema that happens to be empty.
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	seedTaskForTest(t, store, repository.ID)
	seedPlanningProject(t, store, "payer")

	// By number, not the highest version applied. Later migrations exist now,
	// and rolling back whichever one happens to be last would replay a file
	// this test says nothing about.
	//
	// Children before parents: a foreign key cannot outlive the table it
	// points at, so planningTables is walked backwards here.
	for index := len(planningTables) - 1; index >= 0; index-- {
		if _, err := store.db.ExecContext(ctx, `DROP TABLE `+planningTables[index]); err != nil {
			t.Fatalf("roll back %s: %v", planningTables[index], err)
		}
	}
	for _, version := range planningRenameVersions {
		if _, err := store.db.ExecContext(ctx,
			`DELETE FROM schema_migrations WHERE version = ?`, version); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.migrate(ctx); err != nil {
		t.Fatalf("apply 046 and 048 to a database at 045: %v", err)
	}
	for _, table := range planningTables {
		var count int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s did not come back", table)
		}
	}
	// The rows the earlier migrations own are untouched, and the planning
	// tables are empty because 046 creates them and backfills nothing.
	var tasks, projects int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks == 0 {
		t.Error("applying 046 lost the rows an earlier migration owned")
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM planning_projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Errorf("planning_projects = %d rows, want a table 046 created empty", projects)
	}
	// The upgraded database is writable through the same store methods.
	seedPlanningProject(t, store, "payer")
	if _, err := store.PlanningProject(ctx, "payer"); err != nil {
		t.Fatalf("write to the upgraded database: %v", err)
	}
}

// Re-running the ledger against an already-migrated database is a no-op, which
// is what makes a restart safe.
func TestMigration046IsIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	if err := store.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if _, err := store.PlanningProject(ctx, "payer"); err != nil {
		t.Fatalf("a second migrate pass dropped a saved project: %v", err)
	}
}

// rollBack048 puts the planning schema back into the shape 046 left it in: a
// planning_boulders table, a boulder_id on it and on planning_pebbles, and the
// two indexes under their old names. It is the exact inverse of 048 and moves
// no rows, so whatever was seeded before it is what 048 is handed when the
// ledger row is gone and migrate runs again.
func rollBack048(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`DROP INDEX planning_stones_order`,
		`DROP INDEX planning_pebbles_stone`,
		`ALTER TABLE planning_pebbles RENAME COLUMN stone_id TO boulder_id`,
		`ALTER TABLE planning_stones RENAME COLUMN stone_id TO boulder_id`,
		`ALTER TABLE planning_stones RENAME TO planning_boulders`,
		`CREATE INDEX planning_boulders_order ON planning_boulders(project, number, ordinal)`,
		`CREATE INDEX planning_pebbles_boulder ON planning_pebbles(project, number, boulder_id)`,
		`DELETE FROM schema_migrations WHERE version = 48`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("roll back 048 with %q: %v", statement, err)
		}
	}
}

// schemaHolds reports whether sqlite_master holds an object of that name,
// which is how these tests ask what a rename left behind.
func schemaHolds(t *testing.T, store *Store, name string) bool {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count > 0
}

// The rename is the whole of 048. The boulder names are gone, the stone names
// are there, and that includes the indexes, because an index still called
// planning_boulders_order on a table called planning_stones is exactly the
// confusion this migration exists to end.
func TestMigration048RenamesTheBoulderNamesToStone(t *testing.T) {
	store := newTestStore(t)
	gone := []string{"planning_boulders", "planning_boulders_order", "planning_pebbles_boulder"}
	for _, name := range gone {
		if schemaHolds(t, store, name) {
			t.Errorf("%s is still in the schema after 048", name)
		}
	}
	for _, name := range []string{"planning_stones", "planning_stones_order", "planning_pebbles_stone"} {
		if !schemaHolds(t, store, name) {
			t.Errorf("%s was not created by 048", name)
		}
	}
	var ddl string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'planning_pebbles'`,
	).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ddl, "boulder_id") {
		t.Errorf("planning_pebbles still carries a boulder_id:\n%s", ddl)
	}
	if !strings.Contains(ddl, "stone_id") {
		t.Errorf("planning_pebbles has no stone_id:\n%s", ddl)
	}
}

// A pure rename keeps every row and every link between them. This seeds a real
// split, puts the schema back to the boulder shape without touching a row, and
// lets 048 run for real against it.
func TestMigration048KeepsTheRowsAndTheirLinks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "frozen")
	if _, err := store.ReplacePlanningPebbles(ctx, "payer", 1, SavePlanningPebblesRequest{
		Stones: []PlanningStone{
			{ID: "b1", Ordinal: 1, Title: "Rebuild the driver", Statement: "The driver comes back."},
			{ID: "b2", Ordinal: 2, Title: "Publish it"},
		},
		Pebbles: []PlanningPebble{
			{Ordinal: 1, Slug: "01-driver", Title: "Write the driver", StoneID: "b1"},
			{Ordinal: 2, Slug: "02-publish", Title: "Publish it", StoneID: "b2"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	rollBack048(t, store)
	if err := store.migrate(ctx); err != nil {
		t.Fatalf("apply 048 to a database at 047: %v", err)
	}

	after, err := store.PlanningCheckpoint(ctx, "payer", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Stones) != 2 || len(after.Pebbles) != 2 {
		t.Fatalf("split = %d stones, %d pebbles, want the seeded two of each",
			len(after.Stones), len(after.Pebbles))
	}
	if after.Stones[0].ID != "b1" || after.Stones[0].Statement != "The driver comes back." {
		t.Errorf("first stone = %+v", after.Stones[0])
	}
	if after.Pebbles[1].StoneID != "b2" {
		t.Errorf("second pebble sits in stone %q, want b2", after.Pebbles[1].StoneID)
	}
	// The renamed foreign key still bites: a pebble may not name a stone that
	// is not there.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO planning_pebbles(project, number, ordinal, slug, stone_id, created_at, updated_at)
		 VALUES ('payer', 1, 3, 'third', 'b9', 0, 0)`); err == nil {
		t.Error("a pebble naming a stone that does not exist was accepted")
	}
}

// A database that already holds a planning_stones table did not get it from
// here, and renaming onto it would put two shapes in one name. 048 refuses and
// says so, the way 030 does.
func TestMigration048RefusesWhenPlanningStonesAlreadyExists(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	rollBack048(t, store)
	if _, err := store.db.ExecContext(ctx,
		`CREATE TABLE planning_stones (whatever TEXT)`); err != nil {
		t.Fatal(err)
	}
	err := store.migrate(ctx)
	if err == nil {
		t.Fatal("048 renamed onto a table that was already there")
	}
	if !strings.Contains(err.Error(), "migration 048 refused") {
		t.Errorf("error = %v, want the refusal 048 raises", err)
	}
	// The refusal is a rollback, not a half-applied rename: the boulder names
	// are exactly as they were.
	if !schemaHolds(t, store, "planning_boulders") {
		t.Error("the refused migration took planning_boulders with it")
	}
}
