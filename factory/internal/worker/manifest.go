package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const manifestSchemaVersion = 3
const disposalJournalSchemaVersion = 1

const (
	manifestPreparing       = "preparing"
	manifestWorktreeCreated = "worktree_created"
	manifestSupervisorReady = "supervisor_ready"
	manifestRunning         = "running"
	manifestCompleted       = "completed"
	manifestRetained        = "retained"
	manifestNotCreated      = "not_created"
	manifestInconsistent    = "inconsistent"
	manifestMissing         = "missing"
	manifestCleanupStarted  = "cleanup_started"
	manifestCleaned         = "cleaned"
)

const (
	cleanupIntentAutomatic = "automatic"
	cleanupIntentOperator  = "operator_confirmed"
)

type attemptManifest struct {
	SchemaVersion int `json:"schema_version"`

	WorkerID    string `json:"worker_id"`
	SessionID   string `json:"session_id"`
	ExecutionID string `json:"execution_id"`
	AttemptID   string `json:"attempt_id"`

	RepositoryID   string `json:"repository_id"`
	RepositoryKey  string `json:"repository_key"`
	RepositoryPath string `json:"repository_path"`
	RemoteIdentity string `json:"remote_identity"`
	BaseBranch     string `json:"base_branch,omitempty"`
	BaseCommit     string `json:"base_commit"`
	WorktreePath   string `json:"worktree_path"`
	Branch         string `json:"branch"`

	SupervisorPID        int64     `json:"supervisor_pid,omitempty"`
	SupervisorIdentity   string    `json:"supervisor_identity,omitempty"`
	ProcessGroupID       int64     `json:"process_group_id,omitempty"`
	ProcessGroupIdentity string    `json:"process_group_identity,omitempty"`
	ProcessActive        bool      `json:"process_active"`
	LeaseDeadline        time.Time `json:"lease_deadline,omitempty"`

	Lifecycle       string    `json:"lifecycle"`
	TerminalState   string    `json:"terminal_state,omitempty"`
	RetentionReason string    `json:"retention_reason,omitempty"`
	CleanupIntent   string    `json:"cleanup_intent,omitempty"`
	CleanupResult   string    `json:"cleanup_result,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type attemptManifestV1 struct {
	attemptManifest
	TaskID string `json:"task_id"`
}

type attemptManifestV2 struct {
	attemptManifest
	WorkTargetID string `json:"work_target_id"`
}

type disposalJournal struct {
	SchemaVersion int      `json:"schema_version"`
	WorkerID      string   `json:"worker_id"`
	AttemptIDs    []string `json:"attempt_ids"`
}

type manifestStore struct {
	dataDirectory string
	workerID      string
	now           func() time.Time

	mutex          sync.Mutex
	hook           func(string) error
	disposalHook   func(string) error
	directoryReady bool
}

func newManifestStore(dataDirectory, workerID string) *manifestStore {
	return &manifestStore{dataDirectory: dataDirectory, workerID: workerID, now: time.Now}
}

func (store *manifestStore) attemptsDirectory() string {
	return filepath.Join(store.dataDirectory, "attempts")
}

func (store *manifestStore) path(attemptID string) (string, error) {
	if !uuidPattern.MatchString(attemptID) {
		return "", errors.New("invalid attempt ID")
	}
	return filepath.Join(store.attemptsDirectory(), attemptID+".json"), nil
}

func (store *manifestStore) create(manifest attemptManifest) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	path, err := store.path(manifest.AttemptID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("attempt manifest already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect attempt manifest: %w", err)
	}
	now := store.now().UTC()
	manifest.SchemaVersion = manifestSchemaVersion
	manifest.WorkerID = store.workerID
	manifest.CreatedAt = now
	manifest.UpdatedAt = now
	if err := store.validate(manifest); err != nil {
		return err
	}
	return store.writeLocked(path, manifest)
}

func (store *manifestStore) update(attemptID string, change func(*attemptManifest) error) (attemptManifest, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	path, err := store.path(attemptID)
	if err != nil {
		return attemptManifest{}, err
	}
	manifest, err := store.readLocked(path)
	if err != nil {
		return attemptManifest{}, err
	}
	if err := change(&manifest); err != nil {
		return attemptManifest{}, err
	}
	manifest.UpdatedAt = store.now().UTC()
	if err := store.validate(manifest); err != nil {
		return attemptManifest{}, err
	}
	if err := store.writeLocked(path, manifest); err != nil {
		return attemptManifest{}, err
	}
	return manifest, nil
}

func (store *manifestStore) load(attemptID string) (attemptManifest, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	path, err := store.path(attemptID)
	if err != nil {
		return attemptManifest{}, err
	}
	return store.readLocked(path)
}

func (store *manifestStore) loadAll() ([]attemptManifest, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	directory, err := store.ensureDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read attempt manifests: %w", err)
	}
	manifests := make([]attemptManifest, 0, len(entries))
	removedTemporary := false
	var loadErrors []error
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") && strings.HasSuffix(entry.Name(), ".tmp") {
			info, infoErr := entry.Info()
			if infoErr != nil {
				loadErrors = append(loadErrors, fmt.Errorf("inspect stale temporary manifest: %w", infoErr))
				continue
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				loadErrors = append(loadErrors, fmt.Errorf("unsafe stale temporary manifest entry: %s", entry.Name()))
				continue
			}
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				loadErrors = append(loadErrors, fmt.Errorf("remove stale temporary manifest: %w", err))
				continue
			}
			removedTemporary = true
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			loadErrors = append(loadErrors,
				fmt.Errorf("unexpected entry in attempt manifest directory: %s", entry.Name()))
			continue
		}
		attemptID := strings.TrimSuffix(entry.Name(), ".json")
		path, pathErr := store.path(attemptID)
		if pathErr != nil {
			loadErrors = append(loadErrors, fmt.Errorf("invalid attempt manifest filename %q", entry.Name()))
			continue
		}
		manifest, readErr := store.readLocked(path)
		if readErr != nil {
			loadErrors = append(loadErrors, fmt.Errorf("%s: %w", entry.Name(), readErr))
			continue
		}
		manifests = append(manifests, manifest)
	}
	if removedTemporary {
		if err := syncDirectory(directory); err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("sync stale manifest cleanup: %w", err))
		}
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].AttemptID < manifests[j].AttemptID
	})
	return manifests, errors.Join(loadErrors...)
}

func (store *manifestStore) loadDisposals() ([]string, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	journal, err := store.readDisposalsLocked()
	if err != nil {
		return nil, err
	}
	return append([]string(nil), journal.AttemptIDs...), nil
}

func (store *manifestStore) addDisposal(attemptID string) error {
	if !uuidPattern.MatchString(attemptID) {
		return errors.New("invalid disposed attempt ID")
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	journal, err := store.readDisposalsLocked()
	if err != nil {
		return err
	}
	for _, existing := range journal.AttemptIDs {
		if existing == attemptID {
			return nil
		}
	}
	if store.disposalHook != nil {
		if err := store.disposalHook("before_add"); err != nil {
			return err
		}
	}
	journal.AttemptIDs = append(journal.AttemptIDs, attemptID)
	sort.Strings(journal.AttemptIDs)
	return store.writeDisposalsLocked(journal)
}

func (store *manifestStore) clearDisposals(attemptIDs []string) error {
	if len(attemptIDs) == 0 {
		return nil
	}
	cleared := make(map[string]bool, len(attemptIDs))
	for _, attemptID := range attemptIDs {
		if !uuidPattern.MatchString(attemptID) {
			return errors.New("invalid disposed attempt ID")
		}
		cleared[attemptID] = true
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	journal, err := store.readDisposalsLocked()
	if err != nil {
		return err
	}
	remaining := journal.AttemptIDs[:0]
	for _, attemptID := range journal.AttemptIDs {
		if !cleared[attemptID] {
			remaining = append(remaining, attemptID)
		}
	}
	journal.AttemptIDs = remaining
	return store.writeDisposalsLocked(journal)
}

func (store *manifestStore) removeDisposedManifests(attemptIDs []string) error {
	if len(attemptIDs) == 0 {
		return nil
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	removed := false
	for _, attemptID := range attemptIDs {
		path, err := store.path(attemptID)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect acknowledged attempt manifest: %w", err)
		}
		manifest, err := store.readLocked(path)
		if err != nil {
			return err
		}
		switch manifest.Lifecycle {
		case manifestCleaned, manifestNotCreated, manifestMissing:
		default:
			return fmt.Errorf(
				"refusing to remove disposal manifest in lifecycle %q", manifest.Lifecycle)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove acknowledged disposal manifest: %w", err)
		}
		removed = true
	}
	if removed {
		if err := syncDirectory(store.attemptsDirectory()); err != nil {
			return fmt.Errorf("sync acknowledged disposal manifest removal: %w", err)
		}
	}
	return nil
}

func (store *manifestStore) disposalJournalPath() string {
	return filepath.Join(store.dataDirectory, "disposed-attempts.json")
}

func (store *manifestStore) readDisposalsLocked() (disposalJournal, error) {
	journal := disposalJournal{
		SchemaVersion: disposalJournalSchemaVersion,
		WorkerID:      store.workerID,
		AttemptIDs:    []string{},
	}
	entries, err := os.ReadDir(store.dataDirectory)
	if err != nil {
		return disposalJournal{}, fmt.Errorf("read worker data directory for disposal journal: %w", err)
	}
	removedTemporary := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".disposed-attempts-") ||
			!strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return disposalJournal{}, fmt.Errorf("inspect stale disposal journal: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return disposalJournal{}, fmt.Errorf("unsafe stale disposal journal entry: %s", entry.Name())
		}
		if err := os.Remove(filepath.Join(store.dataDirectory, entry.Name())); err != nil {
			return disposalJournal{}, fmt.Errorf("remove stale disposal journal: %w", err)
		}
		removedTemporary = true
	}
	if removedTemporary {
		if err := syncDirectory(store.dataDirectory); err != nil {
			return disposalJournal{}, fmt.Errorf("sync stale disposal journal cleanup: %w", err)
		}
	}
	path := store.disposalJournalPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return disposalJournal{}, fmt.Errorf("inspect disposal journal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return disposalJournal{}, errors.New("disposal journal must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return disposalJournal{}, errors.New("disposal journal permissions must not allow group or other access")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return disposalJournal{}, fmt.Errorf("read disposal journal: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return disposalJournal{}, fmt.Errorf("decode disposal journal: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return disposalJournal{}, errors.New("disposal journal contains trailing JSON")
	}
	if journal.SchemaVersion != disposalJournalSchemaVersion {
		return disposalJournal{}, fmt.Errorf(
			"unsupported disposal journal schema version %d", journal.SchemaVersion)
	}
	if journal.WorkerID != store.workerID {
		return disposalJournal{}, errors.New("disposal journal belongs to a different worker")
	}
	seen := make(map[string]bool, len(journal.AttemptIDs))
	for _, attemptID := range journal.AttemptIDs {
		if !uuidPattern.MatchString(attemptID) || seen[attemptID] {
			return disposalJournal{}, errors.New("disposal journal attempt IDs must be unique UUIDs")
		}
		seen[attemptID] = true
	}
	sort.Strings(journal.AttemptIDs)
	return journal, nil
}

func (store *manifestStore) writeDisposalsLocked(journal disposalJournal) error {
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode disposal journal: %w", err)
	}
	body = append(body, '\n')
	file, err := os.CreateTemp(store.dataDirectory, ".disposed-attempts-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary disposal journal: %w", err)
	}
	temporary := file.Name()
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary disposal journal: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("write temporary disposal journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary disposal journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary disposal journal: %w", err)
	}
	if err := os.Rename(temporary, store.disposalJournalPath()); err != nil {
		return fmt.Errorf("replace disposal journal: %w", err)
	}
	removeTemporary = false
	if err := syncDirectory(store.dataDirectory); err != nil {
		return fmt.Errorf("sync worker data directory after disposal journal update: %w", err)
	}
	return nil
}

func (store *manifestStore) readLocked(path string) (attemptManifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return attemptManifest{}, errors.New("attempt manifest does not exist")
		}
		return attemptManifest{}, fmt.Errorf("inspect attempt manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return attemptManifest{}, errors.New("attempt manifest must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return attemptManifest{}, errors.New("attempt manifest permissions must not allow group or other access")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return attemptManifest{}, fmt.Errorf("read attempt manifest: %w", err)
	}
	var manifest attemptManifest
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(body, &header); err != nil {
		return attemptManifest{}, fmt.Errorf("decode attempt manifest header: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if header.SchemaVersion == 1 {
		var legacy attemptManifestV1
		if err := decoder.Decode(&legacy); err != nil {
			return attemptManifest{}, fmt.Errorf("decode version 1 attempt manifest: %w", err)
		}
		manifest = legacy.attemptManifest
		manifest.SchemaVersion = manifestSchemaVersion
		manifest.SessionID = legacy.TaskID
	} else if header.SchemaVersion == 2 {
		var legacy attemptManifestV2
		if err := decoder.Decode(&legacy); err != nil {
			return attemptManifest{}, fmt.Errorf("decode version 2 attempt manifest: %w", err)
		}
		manifest = legacy.attemptManifest
		manifest.SchemaVersion = manifestSchemaVersion
		manifest.SessionID = legacy.WorkTargetID
	} else if err := decoder.Decode(&manifest); err != nil {
		return attemptManifest{}, fmt.Errorf("decode attempt manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return attemptManifest{}, errors.New("attempt manifest contains trailing JSON")
	}
	if err := store.validate(manifest); err != nil {
		return attemptManifest{}, err
	}
	return manifest, nil
}

func (store *manifestStore) writeLocked(path string, manifest attemptManifest) error {
	directory, err := store.ensureDirectory()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode attempt manifest: %w", err)
	}
	body = append(body, '\n')
	file, err := os.CreateTemp(directory, "."+manifest.AttemptID+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary attempt manifest: %w", err)
	}
	temporary := file.Name()
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary attempt manifest: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("write temporary attempt manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary attempt manifest: %w", err)
	}
	if err := store.callHook("after_file_sync"); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary attempt manifest: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace attempt manifest: %w", err)
	}
	removeTemporary = false
	if err := store.callHook("after_rename"); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync attempt manifest directory: %w", err)
	}
	return store.callHook("after_directory_sync")
}

func (store *manifestStore) ensureDirectory() (string, error) {
	directory := store.attemptsDirectory()
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return "", fmt.Errorf("create attempt manifest directory: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect attempt manifest directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("inspect attempt manifest directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("attempt manifest directory must be a real directory, not a symlink")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("protect attempt manifest directory: %w", err)
	}
	if !store.directoryReady {
		if err := store.callHook("before_attempts_parent_sync"); err != nil {
			return "", err
		}
		if err := syncDirectory(store.dataDirectory); err != nil {
			return "", fmt.Errorf("sync worker data directory after creating attempts: %w", err)
		}
		if err := store.callHook("after_attempts_parent_sync"); err != nil {
			return "", err
		}
		store.directoryReady = true
	}
	return directory, nil
}

func (store *manifestStore) callHook(stage string) error {
	if store.hook == nil {
		return nil
	}
	return store.hook(stage)
}

func (store *manifestStore) validate(manifest attemptManifest) error {
	if manifest.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("unsupported attempt manifest schema version %d", manifest.SchemaVersion)
	}
	for name, value := range map[string]string{
		"worker_id": manifest.WorkerID, "session_id": manifest.SessionID,
		"execution_id": manifest.ExecutionID, "attempt_id": manifest.AttemptID,
		"repository_id": manifest.RepositoryID,
	} {
		if !uuidPattern.MatchString(value) {
			return fmt.Errorf("attempt manifest %s is not a valid UUID", name)
		}
	}
	if manifest.WorkerID != store.workerID {
		return errors.New("attempt manifest belongs to a different worker")
	}
	if strings.TrimSpace(manifest.RepositoryKey) == "" || strings.TrimSpace(manifest.RemoteIdentity) == "" {
		return errors.New("attempt manifest repository identity is incomplete")
	}
	if !filepath.IsAbs(manifest.RepositoryPath) || filepath.Clean(manifest.RepositoryPath) != manifest.RepositoryPath {
		return errors.New("attempt manifest repository path is not canonical")
	}
	if !commitPattern.MatchString(manifest.BaseCommit) {
		return errors.New("attempt manifest base commit is invalid")
	}
	expectedPath := filepath.Join(store.dataDirectory, "worktrees", manifest.AttemptID)
	if manifest.WorktreePath != expectedPath {
		return errors.New("attempt manifest worktree path is not the owned Factory path")
	}
	expectedBranch := "factory/" + manifest.SessionID[:12] + "-" + manifest.AttemptID[:12]
	previewBranch := "factory-v2/" + manifest.SessionID[:12] + "-" + manifest.AttemptID[:12]
	if manifest.Branch != expectedBranch && manifest.Branch != previewBranch {
		return errors.New("attempt manifest branch does not match its Session and attempt")
	}
	allowedLifecycle := map[string]bool{
		manifestPreparing: true, manifestWorktreeCreated: true, manifestSupervisorReady: true,
		manifestRunning: true, manifestCompleted: true, manifestRetained: true,
		manifestNotCreated: true, manifestInconsistent: true, manifestMissing: true,
		manifestCleanupStarted: true, manifestCleaned: true,
	}
	if !allowedLifecycle[manifest.Lifecycle] {
		return fmt.Errorf("attempt manifest lifecycle %q is invalid", manifest.Lifecycle)
	}
	if manifest.CleanupIntent != "" &&
		manifest.CleanupIntent != cleanupIntentAutomatic &&
		manifest.CleanupIntent != cleanupIntentOperator {
		return fmt.Errorf("attempt manifest cleanup intent %q is invalid", manifest.CleanupIntent)
	}
	processEmpty := manifest.SupervisorPID == 0 && manifest.SupervisorIdentity == "" &&
		manifest.ProcessGroupID == 0 && manifest.ProcessGroupIdentity == ""
	processComplete := manifest.SupervisorPID > 0 && manifest.SupervisorIdentity != "" &&
		manifest.ProcessGroupID > 0 && manifest.ProcessGroupIdentity != ""
	if !processEmpty && !processComplete {
		return errors.New("attempt manifest process identity is partial")
	}
	if manifest.ProcessActive && !processComplete {
		return errors.New("attempt manifest active process identity is incomplete")
	}
	if manifest.CreatedAt.IsZero() || manifest.UpdatedAt.IsZero() {
		return errors.New("attempt manifest timestamps are incomplete")
	}
	return nil
}
