-- factory: foreign-keys-off

DROP TRIGGER IF EXISTS automation_occurrence_automation_immutable;
DROP TRIGGER IF EXISTS automation_issue_occurrence_type_guard;
DROP TRIGGER IF EXISTS automation_pull_request_occurrence_type_guard;
DROP TRIGGER IF EXISTS automation_schedule_occurrence_type_guard;

CREATE TABLE automation_occurrences_new (
    id TEXT PRIMARY KEY,
    automation_id TEXT NOT NULL REFERENCES automations(id),
    automation_version INTEGER NOT NULL,
    automation_name TEXT NOT NULL,
    workflow_revision_id TEXT REFERENCES workflow_revisions(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    repository_identity TEXT NOT NULL,
    context TEXT,
    timeout_seconds INTEGER,
    state TEXT NOT NULL CHECK (state IN (
        'pending', 'dispatching', 'dispatched', 'task_deleted', 'skipped', 'failed'
    )),
    resolved_prompt TEXT,
    task_request_key TEXT NOT NULL UNIQUE,
    task_id TEXT UNIQUE REFERENCES tasks(id) ON DELETE RESTRICT,
    task_id_snapshot TEXT NOT NULL DEFAULT '',
    diagnostic TEXT NOT NULL DEFAULT '',
    retry_at INTEGER,
    legacy_task_request_json BLOB,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

INSERT INTO automation_occurrences_new(
    id, automation_id, automation_version, automation_name,
    workflow_revision_id, repository_id, repository_identity, context,
    timeout_seconds, state, resolved_prompt, task_request_key, task_id,
    task_id_snapshot, diagnostic, retry_at, created_at, updated_at
)
SELECT
    id, automation_id, automation_version, automation_name,
    workflow_revision_id, repository_id, repository_identity, context,
    timeout_seconds, state, resolved_prompt, task_request_key, task_id,
    task_id_snapshot, diagnostic, retry_at, created_at, updated_at
FROM automation_occurrences;

DROP TABLE automation_occurrences;
ALTER TABLE automation_occurrences_new RENAME TO automation_occurrences;

CREATE INDEX automation_occurrences_pending
ON automation_occurrences(state, retry_at, created_at, id);

CREATE INDEX automation_occurrences_history
ON automation_occurrences(automation_id, created_at DESC, id DESC);

CREATE TRIGGER automation_occurrence_automation_immutable
BEFORE UPDATE OF automation_id ON automation_occurrences
WHEN NEW.automation_id != OLD.automation_id
BEGIN
    SELECT RAISE(ABORT, 'Automation Occurrence identity is immutable');
END;

CREATE TRIGGER automation_issue_occurrence_type_guard
BEFORE INSERT ON automation_github_issue_occurrences
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_issue'
  OR NOT EXISTS (
      SELECT 1 FROM automation_occurrences occurrence
      WHERE occurrence.id = NEW.occurrence_id AND occurrence.automation_id = NEW.automation_id
  )
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_occurrences WHERE occurrence_id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'GitHub issue Occurrence does not match Automation type');
END;

CREATE TRIGGER automation_pull_request_occurrence_type_guard
BEFORE INSERT ON automation_github_pull_request_occurrences
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_pull_request'
  OR NOT EXISTS (
      SELECT 1 FROM automation_occurrences occurrence
      WHERE occurrence.id = NEW.occurrence_id AND occurrence.automation_id = NEW.automation_id
  )
  OR EXISTS (SELECT 1 FROM automation_github_issue_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_occurrences WHERE occurrence_id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'GitHub pull-request Occurrence does not match Automation type');
END;

CREATE TRIGGER automation_schedule_occurrence_type_guard
BEFORE INSERT ON automation_schedule_occurrences
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'schedule'
  OR NOT EXISTS (
      SELECT 1 FROM automation_occurrences occurrence
      WHERE occurrence.id = NEW.occurrence_id AND occurrence.automation_id = NEW.automation_id
  )
  OR EXISTS (SELECT 1 FROM automation_github_issue_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_occurrences WHERE occurrence_id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'Schedule Occurrence does not match Automation type');
END;

CREATE TABLE legacy_poller_migrations (
    id TEXT PRIMARY KEY,
    snapshot_digest TEXT NOT NULL,
    config_path TEXT NOT NULL,
    data_home TEXT NOT NULL,
    working_directory TEXT NOT NULL,
    data_directory TEXT NOT NULL,
    ledger_path TEXT NOT NULL,
    archive_root TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('previewed', 'imported', 'finalized')),
    archive_path TEXT NOT NULL DEFAULT '',
    queue_count INTEGER NOT NULL DEFAULT 0,
    supported_queue_count INTEGER NOT NULL DEFAULT 0,
    unsupported_queue_count INTEGER NOT NULL DEFAULT 0,
    unimported_pending_observation_count INTEGER NOT NULL DEFAULT 0,
    unimported_submitted_observation_count INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    finalized_at INTEGER
);

CREATE TABLE legacy_poller_imports (
    migration_id TEXT NOT NULL REFERENCES legacy_poller_migrations(id),
    queue_id TEXT NOT NULL,
    queue_name TEXT NOT NULL,
    workflow_id TEXT NOT NULL UNIQUE REFERENCES workflows(id),
    automation_id TEXT NOT NULL UNIQUE REFERENCES automations(id),
    PRIMARY KEY (migration_id, queue_id)
);

CREATE TABLE legacy_poller_observations (
    occurrence_id TEXT PRIMARY KEY REFERENCES automation_occurrences(id),
    migration_id TEXT NOT NULL REFERENCES legacy_poller_migrations(id),
    queue_id TEXT NOT NULL,
    issue_key TEXT NOT NULL,
    source_state TEXT NOT NULL CHECK (source_state IN ('pending', 'submitted')),
    UNIQUE (migration_id, queue_id, issue_key)
);
