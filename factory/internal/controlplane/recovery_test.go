package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
	"github.com/owainlewis/factory/migrations"
)

func TestBackupAndRestorePreserveDurableControlPlaneState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source", "factory.sqlite3")
	store, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Recovery task", Prompt: "Preserve this Run.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 1,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "recovery-run"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{RequestID: "recovery-claim", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if err := store.AppendEvents(ctx, claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events: []protocol.AttemptEvent{{
			Sequence: 0, Kind: "progress", Payload: json.RawMessage(`{"text":"durable recovery event"}`),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(root, "backups", "factory.sqlite3")
	if err := BackupDatabase(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{backupPath, backupPath + ".v2-control-plane"} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("backup mode for %s = %o, want 600", path, info.Mode().Perm())
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(backupPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("standalone backup has unexpected %s file: %v", suffix, err)
		}
	}

	restoredPath := filepath.Join(root, "restored-home", "server", "factory.sqlite3")
	if err := RestoreBackup(ctx, backupPath, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatalf("start restored database: %v", err)
	}
	defer restored.Close()
	if _, err := restored.SweepExpired(ctx); err != nil {
		t.Fatalf("run restored server startup sweep: %v", err)
	}

	restoredRun, err := restored.Run(ctx, run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restoredRun.Sessions) != 1 || len(restoredRun.Sessions[0].Attempts) != 1 ||
		restoredRun.Sessions[0].Attempts[0].ID != claim.Attempt.ID {
		t.Fatalf("restored Run = %#v", restoredRun)
	}
	events, err := restored.Events(ctx, claim.Attempt.ID, -1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || string(events.Events[0].Payload) != `{"text":"durable recovery event"}` {
		t.Fatalf("restored events = %#v", events.Events)
	}
	if _, err := restored.Task(ctx, task.ID); err != nil {
		t.Fatalf("restore Task: %v", err)
	}
}

func TestRestoreRejectsCorruptBackupAndExistingDestination(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite3")
	store, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(root, "backup.sqlite3")
	if err := BackupDatabase(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(root, "existing.sqlite3")
	original := []byte("do not replace")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(ctx, backupPath, destination); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("existing destination error = %v", err)
	}
	after, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("existing restore destination was modified")
	}

	if err := os.WriteFile(backupPath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptDestination := filepath.Join(root, "corrupt-restore.sqlite3")
	if err := RestoreBackup(ctx, backupPath, corruptDestination); err == nil {
		t.Fatal("restored a corrupt backup")
	}
	if _, err := os.Lstat(corruptDestination); !os.IsNotExist(err) {
		t.Fatalf("corrupt restore created destination: %v", err)
	}
}

func TestBackupRequiresExistingCurrentSourceAndDoesNotMutateIt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	missing := filepath.Join(root, "missing.sqlite3")
	destination := filepath.Join(root, "missing-backup.sqlite3")
	if err := BackupDatabase(ctx, missing, destination); err == nil {
		t.Fatal("backed up a missing source database")
	}
	for _, path := range []string{missing, missing + ".v2-control-plane", destination} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("missing-source backup created %s: %v", path, err)
		}
	}

	source := filepath.Join(root, "outdated.sqlite3")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := BackupDatabase(ctx, source, filepath.Join(root, "outdated-backup.sqlite3")); err == nil {
		t.Fatal("backed up a database that needs migration")
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("read-only backup changed its source database")
	}
}

func TestRestoreRejectsForgedLedgerAndSiblingWAL(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	forged := filepath.Join(root, "forged.sqlite3")
	database, err := sql.Open("sqlite", forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	version := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version++
		if _, err := database.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)`, version); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(forged, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := createDatabaseMarker(forged + ".v2-control-plane"); err != nil {
		t.Fatal(err)
	}
	forgedDestination := filepath.Join(root, "forged-restored.sqlite3")
	if err := RestoreBackup(ctx, forged, forgedDestination); err == nil || !strings.Contains(err.Error(), "schema does not match") {
		t.Fatalf("forged migration ledger restore error = %v", err)
	}
	if _, err := os.Lstat(forgedDestination); !os.IsNotExist(err) {
		t.Fatalf("forged restore published a destination: %v", err)
	}

	source := filepath.Join(root, "source.sqlite3")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "standalone.sqlite3")
	if err := BackupDatabase(ctx, source, backup); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	walDatabase, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer walDatabase.Close()
	if _, err := walDatabase.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := walDatabase.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := walDatabase.Exec(`CREATE TABLE injected (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	walDestination := filepath.Join(root, "wal-restored.sqlite3")
	if err := RestoreBackup(ctx, backup, walDestination); err == nil || !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("sibling WAL restore error = %v", err)
	}
	if _, err := os.Lstat(walDestination); !os.IsNotExist(err) {
		t.Fatalf("WAL restore published a destination: %v", err)
	}
}

func TestRestoreRejectsEmptyMigrationLedger(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backup := filepath.Join(root, "empty-ledger.sqlite3")
	database, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backup, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := createDatabaseMarker(backup + ".v2-control-plane"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored.sqlite3")
	if err := RestoreBackup(ctx, backup, destination); err == nil || !strings.Contains(err.Error(), "ledger is empty") {
		t.Fatalf("empty ledger restore error = %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("empty ledger restore published a destination: %v", err)
	}
}

func TestBackupRecoversInterruptedPartialPublication(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source", "factory.sqlite3")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	destinationDirectory := filepath.Join(root, "backups")
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	stagingDirectory, err := os.MkdirTemp(destinationDirectory, ".factory-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stagingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(stagingDirectory, "factory.sqlite3")
	if err := createSnapshot(ctx, store.db, staged); err != nil {
		t.Fatal(err)
	}
	if err := createDatabaseMarker(staged + ".v2-control-plane"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(destinationDirectory, "factory.sqlite3")
	if err := os.Link(staged, destination); err != nil {
		t.Fatal(err)
	}

	if err := BackupDatabase(ctx, source, destination); err != nil {
		t.Fatalf("retry interrupted backup: %v", err)
	}
	if _, _, err := validateRecoverySource(destination, true); err != nil {
		t.Fatalf("validate retried backup: %v", err)
	}
	if _, err := os.Lstat(stagingDirectory); !os.IsNotExist(err) {
		t.Fatalf("interrupted staging directory remains: %v", err)
	}
}

func TestBackupRejectsWritableDestinationDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source.sqlite3")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	writable := filepath.Join(root, "writable")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(writable, "backup.sqlite3")
	if err := BackupDatabase(ctx, source, destination); err == nil || !strings.Contains(err.Error(), "owner-controlled") {
		t.Fatalf("writable destination error = %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("writable destination received a backup: %v", err)
	}
}
