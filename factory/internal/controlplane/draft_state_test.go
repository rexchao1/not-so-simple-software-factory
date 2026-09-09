package controlplane

import (
	"context"
	"strings"
	"testing"
)

func TestMigrationAllowsDraftSessionState(t *testing.T) {
	store := newTestStore(t)
	var allowed int
	err := store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM pragma_table_info('sessions')
		WHERE name IN ('approved_by', 'approved_at', 'delivery')
	`).Scan(&allowed)
	if err != nil {
		t.Fatal(err)
	}
	if allowed != 3 {
		t.Fatalf("approval and delivery columns present = %d, want 3", allowed)
	}
	var sessionsDDL string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sessions'
	`).Scan(&sessionsDDL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sessionsDDL, "'draft'") {
		t.Fatalf("sessions state CHECK does not admit draft:\n%s", sessionsDDL)
	}
	var runsDDL string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'runs'
	`).Scan(&runsDDL); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"'orchestrator'", "'cockpit'", "'github'"} {
		if !strings.Contains(runsDDL, source) {
			t.Fatalf("runs source CHECK missing %s:\n%s", source, runsDDL)
		}
	}
}

func TestMigrationPreservesSessionIndexes(t *testing.T) {
	store := newTestStore(t)
	rows, err := store.db.QueryContext(context.Background(), `
		SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'sessions'
		  AND name NOT LIKE 'sqlite_autoindex%'
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	for _, name := range []string{
		"sessions_run_order", "sessions_claim_order", "sessions_backend_claim",
		"sessions_predecessor", "sessions_run_target_key",
		"sessions_run_target_position", "sessions_retry_identity",
	} {
		if !found[name] {
			t.Errorf("index %s was not recreated by migration 035", name)
		}
	}
}
