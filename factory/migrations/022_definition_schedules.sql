-- factory: foreign-keys-off

DROP TRIGGER IF EXISTS automation_trigger_type_immutable;
DROP TRIGGER IF EXISTS automation_issue_trigger_type_guard;
DROP TRIGGER IF EXISTS automation_pull_request_trigger_type_guard;
DROP TRIGGER IF EXISTS automation_schedule_trigger_type_guard;
DROP TRIGGER IF EXISTS automation_issue_occurrence_type_guard;
DROP TRIGGER IF EXISTS automation_pull_request_occurrence_type_guard;
DROP TRIGGER IF EXISTS automation_schedule_occurrence_type_guard;

CREATE TABLE automations_new (
    id TEXT PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    request_digest BLOB NOT NULL,
    title TEXT NOT NULL,
    title_key TEXT NOT NULL UNIQUE,
    workflow_id TEXT REFERENCES workflows(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    context TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 28800),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    trigger_type TEXT NOT NULL CHECK (trigger_type IN ('github_issue', 'github_pull_request', 'schedule')),
    evaluation_token TEXT,
    evaluation_started_at INTEGER,
    last_checked_at INTEGER,
    next_check_at INTEGER,
    health_status TEXT NOT NULL DEFAULT 'disabled',
    health_code TEXT NOT NULL DEFAULT '',
    health_message TEXT NOT NULL DEFAULT 'Automation is disabled.',
    matched_count INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0,
    dispatched_count INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

INSERT INTO automations_new(
    id, request_key, request_digest, title, title_key, workflow_id,
    repository_id, context, timeout_seconds, enabled, version, trigger_type,
    evaluation_token, evaluation_started_at, last_checked_at, next_check_at,
    health_status, health_code, health_message, matched_count, skipped_count,
    dispatched_count, created_at, updated_at
)
SELECT
    id, request_key, request_digest, title, title_key, workflow_id,
    repository_id, context, timeout_seconds, enabled, version, trigger_type,
    evaluation_token, evaluation_started_at, last_checked_at, next_check_at,
    health_status, health_code, health_message, matched_count, skipped_count,
    dispatched_count, created_at, updated_at
FROM automations;

DROP TABLE automations;
ALTER TABLE automations_new RENAME TO automations;

ALTER TABLE automation_schedule_triggers
ADD COLUMN definition_id TEXT REFERENCES definitions(id);

ALTER TABLE automation_schedule_triggers
ADD COLUMN parameters_json BLOB NOT NULL DEFAULT '{}';

ALTER TABLE automation_schedule_triggers
ADD COLUMN concurrency_limit INTEGER NOT NULL DEFAULT 3
CHECK (concurrency_limit BETWEEN 1 AND 100);

CREATE TABLE automation_schedule_repositories (
    automation_id TEXT NOT NULL REFERENCES automations(id),
    position INTEGER NOT NULL CHECK (position >= 0),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    PRIMARY KEY (automation_id, repository_id),
    UNIQUE (automation_id, position)
);

CREATE TABLE runs_new (
    id TEXT PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    request_digest BLOB NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('manual', 'schedule')),
    definition_id TEXT NOT NULL REFERENCES definitions(id),
    definition_snapshot TEXT NOT NULL
        CHECK (json_valid(definition_snapshot) AND json_type(definition_snapshot) = 'object'),
    parameters TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(parameters) AND json_type(parameters) = 'object'),
    admitted_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    concurrency_limit INTEGER NOT NULL DEFAULT 3
        CHECK (concurrency_limit BETWEEN 1 AND 100)
);

INSERT INTO runs_new(
    id, request_key, request_digest, source_kind, definition_id,
    definition_snapshot, parameters, admitted_at, updated_at, concurrency_limit
)
SELECT
    id, request_key, request_digest, source_kind, definition_id,
    definition_snapshot, parameters, admitted_at, updated_at, concurrency_limit
FROM runs;

DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;

CREATE INDEX runs_list_order ON runs(admitted_at DESC, id DESC);
CREATE INDEX runs_metrics_definition ON runs(definition_id, admitted_at);

ALTER TABLE automation_schedule_occurrences
ADD COLUMN definition_id TEXT REFERENCES definitions(id);

ALTER TABLE automation_schedule_occurrences
ADD COLUMN definition_snapshot BLOB;

ALTER TABLE automation_schedule_occurrences
ADD COLUMN repository_ids_json BLOB;

ALTER TABLE automation_schedule_occurrences
ADD COLUMN parameters_json BLOB;

ALTER TABLE automation_schedule_occurrences
ADD COLUMN concurrency_limit INTEGER;

ALTER TABLE automation_schedule_occurrences
ADD COLUMN run_id TEXT REFERENCES runs(id);

CREATE TRIGGER automation_trigger_type_immutable
BEFORE UPDATE OF trigger_type ON automations
WHEN NEW.trigger_type != OLD.trigger_type
BEGIN
    SELECT RAISE(ABORT, 'Automation trigger type is immutable');
END;

CREATE TRIGGER automation_issue_trigger_type_guard
BEFORE INSERT ON automation_github_issue_triggers
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_issue'
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_triggers WHERE automation_id = NEW.automation_id)
BEGIN
    SELECT RAISE(ABORT, 'GitHub issue Trigger does not match Automation type');
END;

CREATE TRIGGER automation_pull_request_trigger_type_guard
BEFORE INSERT ON automation_github_pull_request_triggers
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_pull_request'
  OR EXISTS (SELECT 1 FROM automation_github_issue_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_triggers WHERE automation_id = NEW.automation_id)
BEGIN
    SELECT RAISE(ABORT, 'GitHub pull-request Trigger does not match Automation type');
END;

CREATE TRIGGER automation_schedule_trigger_type_guard
BEFORE INSERT ON automation_schedule_triggers
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'schedule'
  OR EXISTS (SELECT 1 FROM automation_github_issue_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_triggers WHERE automation_id = NEW.automation_id)
BEGIN
    SELECT RAISE(ABORT, 'Schedule Trigger does not match Automation type');
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

CREATE INDEX automations_due
ON automations(enabled, next_check_at, id);

CREATE INDEX automation_schedule_repositories_ordered
ON automation_schedule_repositories(automation_id, position);

CREATE UNIQUE INDEX automation_schedule_occurrences_run
ON automation_schedule_occurrences(run_id)
WHERE run_id IS NOT NULL;
