package controlplane

import (
	"context"
	"strings"
	"testing"
)

func TestMigration039AddsTheReviewVerdict(t *testing.T) {
	store := newTestStore(t)
	var ddl string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'session_stages'
	`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "review_verdict") {
		t.Fatalf("session_stages has no review_verdict column:\n%s", ddl)
	}
	for _, verdict := range []string{"'approve'", "'request-changes'", "'blocked'"} {
		if !strings.Contains(ddl, verdict) {
			t.Errorf("review_verdict CHECK does not admit %s", verdict)
		}
	}
	// The empty string is the default and must stay legal: a stage that is not
	// a review stage records no verdict, and INV-8 must fail closed on it.
	if !strings.Contains(ddl, "''") {
		t.Error("review_verdict CHECK does not admit the empty default")
	}
}

func TestMigration039RecordsHowReadyWasVerified(t *testing.T) {
	store := newTestStore(t)
	var count int
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM pragma_table_info('sessions')
		WHERE name IN ('delivery_verified_at', 'delivery_verification_source')
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("verification columns present = %d, want 2", count)
	}
}

func TestMigration039AllowsAMergeRowBesideTheReadyOutcome(t *testing.T) {
	// work_updates_attempt_outcome is UNIQUE(attempt_id) WHERE attempt_id IS
	// NOT NULL AND status != 'running'. A merge row on an attempt that already
	// has a ready row would collide, so 'merged' has to be exempted. This is
	// the concrete reason 039 touches work_updates at all.
	store := newTestStore(t)
	var indexDDL string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master
		WHERE type = 'index' AND name = 'work_updates_attempt_outcome'
	`).Scan(&indexDDL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexDDL, "'merged'") {
		t.Fatalf("the outcome uniqueness index does not exempt a merge row:\n%s", indexDDL)
	}
	var statusDDL string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'work_updates'
	`).Scan(&statusDDL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusDDL, "'merged'") {
		t.Fatalf("work_updates.status CHECK does not admit 'merged':\n%s", statusDDL)
	}
}

func TestMigration039PreservesTheWorkUpdatesTable(t *testing.T) {
	// 039 rebuilds work_updates, because SQLite cannot alter a CHECK in place.
	// A rebuild that silently drops a column, an index, a trigger, or a CHECK
	// is the worst failure mode in this migration: nothing fails at apply time
	// and a constraint simply stops being enforced. This test is the guard.
	//
	// Every expectation below is transcribed from the state at migration 038:
	// 031_work_lifecycle.sql:180-227 for the table, its indexes and its two
	// triggers, plus the checkpoint_published column added by
	// 034_resume_recovery.sql:10.
	store := newTestStore(t)
	ctx := context.Background()

	wantColumns := []string{
		"id", "work_id", "attempt_id", "request_id", "sequence", "status",
		"message", "pull_request_url", "pull_request_head_branch",
		"pull_request_head_sha", "checkpoint_sha", "actor", "accepted_at",
		"checkpoint_published",
	}
	rows, err := store.db.QueryContext(ctx,
		`SELECT name FROM pragma_table_info('work_updates') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotColumns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		gotColumns = append(gotColumns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotColumns, ",") != strings.Join(wantColumns, ",") {
		t.Fatalf("work_updates columns changed across the rebuild:\ngot  = %v\nwant = %v",
			gotColumns, wantColumns)
	}

	// Indexes and triggers, by name and by the substring that gives each its
	// meaning. A rebuild drops all of them along with the table.
	for name, mustContain := range map[string]string{
		"work_updates_attempt_request":  "WHERE attempt_id IS NOT NULL",
		"work_updates_operator_request": "WHERE attempt_id IS NULL",
		"work_updates_attempt_outcome":  "status != 'running'",
		"work_updates_work_order":       "work_id, sequence",
		"work_updates_attempt_limit":    "work update limit reached",
		"work_updates_progress_limit":   "work progress update limit reached",
	} {
		var ddl string
		if err := store.db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE name = ?`, name).Scan(&ddl); err != nil {
			t.Errorf("%s did not survive the rebuild: %v", name, err)
			continue
		}
		if !strings.Contains(ddl, mustContain) {
			t.Errorf("%s lost %q across the rebuild:\n%s", name, mustContain, ddl)
		}
	}

	var tableDDL string
	if err := store.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'work_updates'`,
	).Scan(&tableDDL); err != nil {
		t.Fatal(err)
	}
	for _, constraint := range []string{
		"UNIQUE (work_id, sequence)",
		"status != 'ready' OR pull_request_url != ''",
		"status != 'needs-input' OR checkpoint_sha != ''",
		"sequence > 0",
		"BETWEEN 1 AND 200",
	} {
		if !strings.Contains(tableDDL, constraint) {
			t.Errorf("work_updates lost the constraint %q across the rebuild:\n%s",
				constraint, tableDDL)
		}
	}
}
