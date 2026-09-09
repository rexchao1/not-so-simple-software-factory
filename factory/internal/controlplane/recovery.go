package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/owainlewis/factory/migrations"
)

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// BackupDatabase writes a consistent, standalone snapshot of an existing
// Factory database without creating or migrating the source.
func BackupDatabase(ctx context.Context, source, destination string) error {
	source, identity, err := validateRecoverySource(source, false)
	if err != nil {
		return fmt.Errorf("validate live Factory database: %w", err)
	}
	database, err := openRecoveryDatabase(source, false)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := identity.unchanged(source); err != nil {
		return err
	}
	if err := checkSQLiteIntegrity(ctx, database); err != nil {
		return fmt.Errorf("validate live Factory database: %w", err)
	}
	if err := checkMigrationCompatibility(ctx, database); err != nil {
		return fmt.Errorf("validate live Factory database: %w", err)
	}
	if err := validateCurrentSchema(ctx, database); err != nil {
		return fmt.Errorf("validate live Factory database: %w", err)
	}
	if err := publishSnapshot(ctx, database, destination, func(ctx context.Context, staged string) error {
		if err := validatePublishedBackup(ctx, staged); err != nil {
			return err
		}
		return identity.unchanged(source)
	}); err != nil {
		return fmt.Errorf("back up Factory database: %w", err)
	}
	return nil
}

// RestoreBackup validates a standalone Factory backup and publishes it at a
// fresh destination after applying supported migrations in private staging.
func RestoreBackup(ctx context.Context, source, destination string) error {
	source, identity, err := validateRecoverySource(source, true)
	if err != nil {
		return fmt.Errorf("validate Factory database backup: %w", err)
	}
	if samePath(source, destination) {
		return errors.New("backup source and restore destination must differ")
	}
	database, err := openRecoveryDatabase(source, true)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := identity.unchanged(source); err != nil {
		return err
	}
	if err := rejectSQLiteSidecars(source); err != nil {
		return err
	}
	if err := checkSQLiteIntegrity(ctx, database); err != nil {
		return fmt.Errorf("validate Factory database backup: %w", err)
	}
	if err := checkMigrationCompatibility(ctx, database); err != nil {
		return fmt.Errorf("validate Factory database backup: %w", err)
	}
	if err := publishSnapshot(ctx, database, destination, func(ctx context.Context, staged string) error {
		if err := prepareRestoredSnapshot(ctx, staged); err != nil {
			return err
		}
		if err := rejectSQLiteSidecars(source); err != nil {
			return err
		}
		return identity.unchanged(source)
	}); err != nil {
		return fmt.Errorf("restore Factory database: %w", err)
	}
	return nil
}

type snapshotValidator func(context.Context, string) error

func publishSnapshot(
	ctx context.Context,
	database *sql.DB,
	destination string,
	validate snapshotValidator,
) error {
	destination, err := prepareRecoveryDestination(destination)
	if err != nil {
		return err
	}
	stagingDirectory, err := os.MkdirTemp(filepath.Dir(destination), ".factory-recovery-")
	if err != nil {
		return fmt.Errorf("create private recovery staging directory: %w", err)
	}
	if err := os.Chmod(stagingDirectory, 0o700); err != nil {
		_ = os.RemoveAll(stagingDirectory)
		return fmt.Errorf("protect recovery staging directory: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(stagingDirectory)
	}()

	staged := filepath.Join(stagingDirectory, "factory.sqlite3")
	if err := createSnapshot(ctx, database, staged); err != nil {
		return err
	}
	if err := createDatabaseMarker(staged + ".v2-control-plane"); err != nil {
		return err
	}
	if err := validate(ctx, staged); err != nil {
		return err
	}
	if err := syncRecoveryFile(staged); err != nil {
		return err
	}
	if err := syncRecoveryFile(staged + ".v2-control-plane"); err != nil {
		return err
	}
	if err := syncDirectory(stagingDirectory); err != nil {
		return fmt.Errorf("sync recovery staging directory: %w", err)
	}
	if err := ensureFreshRecoveryDestination(destination); err != nil {
		return err
	}
	if err := os.Link(staged, destination); err != nil {
		return fmt.Errorf("publish recovery database without replacement: %w", err)
	}
	if err := os.Link(staged+".v2-control-plane", destination+".v2-control-plane"); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("publish recovery marker without replacement: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		_ = os.Remove(destination + ".v2-control-plane")
		_ = os.Remove(destination)
		return fmt.Errorf("sync recovery directory: %w", err)
	}
	return nil
}

func createSnapshot(ctx context.Context, database *sql.DB, destination string) (returnErr error) {
	file, err := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create snapshot destination: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("close empty snapshot destination: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = os.Remove(destination)
		}
	}()
	if _, err := database.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return fmt.Errorf("create consistent SQLite snapshot: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return fmt.Errorf("protect snapshot destination: %w", err)
	}
	return syncRecoveryFile(destination)
}

func validatePublishedBackup(ctx context.Context, path string) error {
	if _, _, err := validateRecoverySource(path, true); err != nil {
		return err
	}
	database, err := openRecoveryDatabase(path, true)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := checkSQLiteIntegrity(ctx, database); err != nil {
		return err
	}
	if err := checkMigrationCompatibility(ctx, database); err != nil {
		return err
	}
	return validateCurrentSchema(ctx, database)
}

func prepareRestoredSnapshot(ctx context.Context, path string) error {
	store, err := openExistingStore(ctx, path)
	if err != nil {
		return fmt.Errorf("open restored database: %w", err)
	}
	if err := validateCurrentSchema(ctx, store.db); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close restored database: %w", err)
	}
	if err := removePrivateSQLiteSidecars(path); err != nil {
		return err
	}
	return validatePublishedBackup(ctx, path)
}

func prepareRecoveryDestination(path string) (string, error) {
	if path == "" || path == ":memory:" {
		return "", errors.New("recovery destination path is required")
	}
	path, err := canonicalRecoveryPath(path, true, true)
	if err != nil {
		return "", err
	}
	if err := ensureFreshRecoveryDestination(path); err != nil {
		return "", err
	}
	return path, nil
}

func ensureFreshRecoveryDestination(path string) error {
	if err := recoverInterruptedRecoveryPublication(path); err != nil {
		return err
	}
	for _, candidate := range []string{
		path, path + ".v2-control-plane", path + "-wal", path + "-shm",
	} {
		if _, err := os.Lstat(candidate); err == nil {
			return fmt.Errorf("refusing to replace existing recovery destination: %s", candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect recovery destination %s: %w", candidate, err)
		}
	}
	return nil
}

func recoverInterruptedRecoveryPublication(path string) error {
	database, databaseErr := os.Lstat(path)
	markerPath := path + ".v2-control-plane"
	marker, markerErr := os.Lstat(markerPath)
	databaseExists := databaseErr == nil
	markerExists := markerErr == nil
	if databaseErr != nil && !errors.Is(databaseErr, os.ErrNotExist) {
		return fmt.Errorf("inspect interrupted recovery database: %w", databaseErr)
	}
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return fmt.Errorf("inspect interrupted recovery marker: %w", markerErr)
	}
	if databaseExists == markerExists {
		return nil
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect interrupted recovery sidecar: %w", err)
		}
	}

	parent := filepath.Dir(path)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return fmt.Errorf("inspect recovery staging directories: %w", err)
	}
	partialPath := path
	partialInfo := database
	stagedName := "factory.sqlite3"
	if markerExists {
		partialPath = markerPath
		partialInfo = marker
		stagedName += ".v2-control-plane"
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".factory-recovery-") {
			continue
		}
		stagingDirectory := filepath.Join(parent, entry.Name())
		stagingInfo, err := os.Lstat(stagingDirectory)
		if err != nil || !stagingInfo.IsDir() || stagingInfo.Mode()&os.ModeSymlink != 0 ||
			stagingInfo.Mode().Perm()&0o077 != 0 {
			continue
		}
		stat, ok := stagingInfo.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			continue
		}
		stagedPartial, err := os.Lstat(filepath.Join(stagingDirectory, stagedName))
		if err != nil || !stagedPartial.Mode().IsRegular() || !os.SameFile(partialInfo, stagedPartial) {
			continue
		}
		stagedDatabase := filepath.Join(stagingDirectory, "factory.sqlite3")
		if _, err := validateRecoveryFile(stagedDatabase, "staged recovery database", true); err != nil {
			continue
		}
		stagedMarker := stagedDatabase + ".v2-control-plane"
		if _, err := validateRecoveryFile(stagedMarker, "staged recovery marker", true); err != nil {
			continue
		}
		if err := validateRecoveryMarker(stagedMarker); err != nil {
			continue
		}
		if err := os.Remove(partialPath); err != nil {
			return fmt.Errorf("remove interrupted recovery publication: %w", err)
		}
		if err := os.RemoveAll(stagingDirectory); err != nil {
			return fmt.Errorf("remove interrupted recovery staging directory: %w", err)
		}
		if err := syncDirectory(parent); err != nil {
			return fmt.Errorf("sync interrupted recovery cleanup: %w", err)
		}
		return nil
	}
	return nil
}

type recoveryIdentity struct {
	database os.FileInfo
	marker   os.FileInfo
}

func validateRecoverySource(path string, standalone bool) (string, recoveryIdentity, error) {
	if path == "" || path == ":memory:" {
		return "", recoveryIdentity{}, errors.New("recovery source path is required")
	}
	path, err := canonicalRecoveryPath(path, false, false)
	if err != nil {
		return "", recoveryIdentity{}, err
	}
	database, err := validateRecoveryFile(path, "recovery database", standalone)
	if err != nil {
		return "", recoveryIdentity{}, err
	}
	markerPath := path + ".v2-control-plane"
	marker, err := validateRecoveryFile(markerPath, "recovery marker", standalone)
	if err != nil {
		return "", recoveryIdentity{}, err
	}
	if err := validateRecoveryMarker(markerPath); err != nil {
		return "", recoveryIdentity{}, err
	}
	if standalone {
		if err := rejectSQLiteSidecars(path); err != nil {
			return "", recoveryIdentity{}, err
		}
	}
	return path, recoveryIdentity{database: database, marker: marker}, nil
}

func validateRecoveryMarker(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Factory recovery marker: %w", err)
	}
	if string(body) != "factory-v2-control-plane\n" {
		return errors.New("refusing a database with an invalid Factory recovery marker")
	}
	return nil
}

func validateRecoveryFile(path, label string, requirePrivate bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a regular non-symlink file: %s", label, path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("inspect %s owner: unsupported file metadata for %s", label, path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("%s must be owned by effective user %d: %s", label, os.Geteuid(), path)
	}
	if requirePrivate && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s permissions are too open: %s", label, path)
	}
	return info, nil
}

func (identity recoveryIdentity) unchanged(path string) error {
	database, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("reinspect recovery database identity: %w", err)
	}
	marker, err := os.Lstat(path + ".v2-control-plane")
	if err != nil {
		return fmt.Errorf("reinspect recovery marker identity: %w", err)
	}
	if !os.SameFile(identity.database, database) || !os.SameFile(identity.marker, marker) {
		return errors.New("recovery source changed while it was in use")
	}
	return nil
}

func canonicalRecoveryPath(path string, createParent, requireParentOwner bool) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve recovery path: %w", err)
	}
	parent := filepath.Dir(absolute)
	if createParent {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return "", fmt.Errorf("create recovery directory: %w", err)
		}
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("canonicalize recovery directory: %w", err)
	}
	if err := validateRecoveryDirectoryChain(canonicalParent, requireParentOwner); err != nil {
		return "", err
	}
	return filepath.Join(canonicalParent, filepath.Base(absolute)), nil
}

func validateRecoveryDirectoryChain(path string, requireParentOwner bool) error {
	parent := path
	effectiveUID := uint32(os.Geteuid())
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect recovery directory %s: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("recovery path must use real non-symlink directories: %s", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("inspect recovery directory owner: unsupported file metadata for %s", path)
		}
		if path == parent && requireParentOwner {
			if stat.Uid != effectiveUID || info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("recovery destination directory must be owner-controlled and not group or world writable: %s", path)
			}
		} else {
			if stat.Uid != effectiveUID && stat.Uid != 0 {
				return fmt.Errorf("recovery path ancestor must be owned by effective user %d or root: %s", effectiveUID, path)
			}
			if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
				return fmt.Errorf("recovery path ancestor must not be group or world writable unless protected by the sticky bit: %s", path)
			}
		}
		next := filepath.Dir(path)
		if next == path {
			return nil
		}
		path = next
	}
}

func rejectSQLiteSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		candidate := path + suffix
		if _, err := os.Lstat(candidate); err == nil {
			return fmt.Errorf("standalone Factory backup must not have a %s sidecar: %s", suffix, candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect Factory backup sidecar %s: %w", candidate, err)
		}
	}
	return nil
}

func removePrivateSQLiteSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove private restored SQLite sidecar: %w", err)
		}
	}
	return nil
}

func openRecoveryDatabase(path string, immutable bool) (*sql.DB, error) {
	u := &url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	if immutable {
		query.Set("immutable", "1")
	}
	u.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open recovery database: %w", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		return nil, fmt.Errorf("open recovery database: %w", err)
	}
	return database, nil
}

func checkSQLiteIntegrity(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("run SQLite quick check: %w", err)
	}
	defer rows.Close()
	var results []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("read SQLite quick check: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read SQLite quick check: %w", err)
	}
	if len(results) != 1 || results[0] != "ok" {
		return fmt.Errorf("SQLite quick check failed: %s", strings.Join(results, "; "))
	}
	foreignKeys, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("run SQLite foreign key check: %w", err)
	}
	defer foreignKeys.Close()
	if foreignKeys.Next() {
		return errors.New("SQLite foreign key check failed")
	}
	if err := foreignKeys.Err(); err != nil {
		return fmt.Errorf("read SQLite foreign key check: %w", err)
	}
	return nil
}

func checkMigrationCompatibility(ctx context.Context, database *sql.DB) error {
	_, _, err := migrationLedgerVersion(ctx, database)
	return err
}

func migrationLedgerVersion(ctx context.Context, database *sql.DB) (int, int, error) {
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return 0, 0, fmt.Errorf("list supported migrations: %w", err)
	}
	supported := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			supported++
		}
	}
	rows, err := database.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect backup schema version: %w", err)
	}
	defer rows.Close()
	expected := 1
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return 0, 0, fmt.Errorf("read backup schema version: %w", err)
		}
		if version != expected {
			return 0, 0, fmt.Errorf("backup migration ledger is not contiguous at version %d", expected)
		}
		if version > supported {
			return 0, 0, errors.New("backup schema is newer than this Factory binary supports")
		}
		expected++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("read backup schema version: %w", err)
	}
	if expected == 1 {
		return 0, 0, errors.New("backup migration ledger is empty")
	}
	return expected - 1, supported, nil
}

type schemaObject struct {
	typeName  string
	name      string
	tableName string
	sql       string
}

func validateCurrentSchema(ctx context.Context, database *sql.DB) error {
	version, supported, err := migrationLedgerVersion(ctx, database)
	if err != nil {
		return err
	}
	if version != supported {
		return fmt.Errorf("Factory database migration ledger ends at %d, want %d", version, supported)
	}
	actual, err := readSchema(ctx, database)
	if err != nil {
		return err
	}
	expected, err := expectedSchema(ctx)
	if err != nil {
		return err
	}
	if !slices.Equal(actual, expected) {
		return errors.New("Factory database schema does not match its migration ledger")
	}
	return nil
}

func expectedSchema(ctx context.Context) ([]schemaObject, error) {
	directory, err := os.MkdirTemp("", "factory-expected-schema-")
	if err != nil {
		return nil, fmt.Errorf("create expected schema directory: %w", err)
	}
	defer os.RemoveAll(directory)
	store, err := Open(ctx, filepath.Join(directory, "factory.sqlite3"))
	if err != nil {
		return nil, fmt.Errorf("build expected Factory schema: %w", err)
	}
	objects, readErr := readSchema(ctx, store.db)
	closeErr := store.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close expected Factory schema: %w", closeErr)
	}
	return objects, nil
}

func readSchema(ctx context.Context, database *sql.DB) ([]schemaObject, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name
	`)
	if err != nil {
		return nil, fmt.Errorf("read Factory database schema: %w", err)
	}
	defer rows.Close()
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.typeName, &object.name, &object.tableName, &object.sql); err != nil {
			return nil, fmt.Errorf("read Factory database schema: %w", err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read Factory database schema: %w", err)
	}
	return objects, nil
}

func syncRecoveryFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open recovery file for sync: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync recovery file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close recovery file: %w", err)
	}
	return nil
}

func samePath(first, second string) bool {
	firstAbsolute, firstErr := filepath.Abs(first)
	secondAbsolute, secondErr := filepath.Abs(second)
	return firstErr == nil && secondErr == nil && firstAbsolute == secondAbsolute
}
