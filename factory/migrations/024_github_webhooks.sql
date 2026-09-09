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
    trigger_type TEXT NOT NULL CHECK (trigger_type IN ('github_issue', 'github_pull_request', 'schedule', 'github_webhook')),
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

INSERT INTO automations_new SELECT * FROM automations;
DROP TABLE automations;
ALTER TABLE automations_new RENAME TO automations;

CREATE TABLE automation_github_webhook_triggers (
    automation_id TEXT PRIMARY KEY REFERENCES automations(id),
    definition_id TEXT NOT NULL REFERENCES definitions(id),
    actions_json TEXT NOT NULL
        CHECK (json_valid(actions_json) AND json_type(actions_json) = 'array'),
    parameters_json TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(parameters_json) AND json_type(parameters_json) = 'object'),
    concurrency_limit INTEGER NOT NULL DEFAULT 1 CHECK (concurrency_limit = 1)
);

CREATE TABLE github_webhook_deliveries (
    delivery_id TEXT PRIMARY KEY,
    payload_digest BLOB NOT NULL,
    event TEXT NOT NULL,
    action TEXT NOT NULL,
    repository_identity TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL CHECK (pull_request_number > 0),
    pull_request_url TEXT NOT NULL,
    pull_request_title TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    head_commit TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('accepted', 'completed', 'failed')),
    diagnostic TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE automation_github_webhook_occurrences (
    occurrence_id TEXT PRIMARY KEY REFERENCES automation_occurrences(id),
    automation_id TEXT NOT NULL REFERENCES automations(id),
    delivery_id TEXT NOT NULL REFERENCES github_webhook_deliveries(delivery_id),
    event TEXT NOT NULL CHECK (event = 'pull_request'),
    action TEXT NOT NULL CHECK (action IN ('opened', 'synchronize')),
    pull_request_number INTEGER NOT NULL CHECK (pull_request_number > 0),
    pull_request_url TEXT NOT NULL,
    pull_request_title TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    head_commit TEXT NOT NULL,
    definition_id TEXT NOT NULL REFERENCES definitions(id),
    definition_snapshot BLOB NOT NULL,
    parameters_json BLOB NOT NULL,
    run_id TEXT UNIQUE REFERENCES runs(id),
    UNIQUE (automation_id, delivery_id)
);

CREATE TABLE runs_new (
    id TEXT PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    request_digest BLOB NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('manual', 'schedule', 'webhook')),
    definition_id TEXT NOT NULL REFERENCES definitions(id),
    definition_snapshot TEXT NOT NULL
        CHECK (json_valid(definition_snapshot) AND json_type(definition_snapshot) = 'object'),
    parameters TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(parameters) AND json_type(parameters) = 'object'),
    admitted_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    concurrency_limit INTEGER NOT NULL DEFAULT 3 CHECK (concurrency_limit BETWEEN 1 AND 100),
    resolved_prompt TEXT NOT NULL DEFAULT ''
);

INSERT INTO runs_new(
    id, request_key, request_digest, source_kind, definition_id,
    definition_snapshot, parameters, admitted_at, updated_at, concurrency_limit
)
SELECT id, request_key, request_digest, source_kind, definition_id,
       definition_snapshot, parameters, admitted_at, updated_at, concurrency_limit
FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;

CREATE INDEX runs_list_order ON runs(admitted_at DESC, id DESC);
CREATE INDEX runs_metrics_definition ON runs(definition_id, admitted_at);
CREATE INDEX automations_due ON automations(enabled, next_check_at, id);
CREATE INDEX github_webhook_deliveries_history ON github_webhook_deliveries(created_at DESC, delivery_id DESC);
CREATE INDEX automation_github_webhook_occurrences_delivery
ON automation_github_webhook_occurrences(delivery_id, automation_id);

CREATE TRIGGER automation_trigger_type_immutable
BEFORE UPDATE OF trigger_type ON automations
WHEN NEW.trigger_type != OLD.trigger_type
BEGIN SELECT RAISE(ABORT, 'Automation trigger type is immutable'); END;

CREATE TRIGGER automation_issue_trigger_type_guard
BEFORE INSERT ON automation_github_issue_triggers
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_issue'
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_webhook_triggers WHERE automation_id = NEW.automation_id)
BEGIN SELECT RAISE(ABORT, 'GitHub issue Trigger does not match Automation type'); END;

CREATE TRIGGER automation_pull_request_trigger_type_guard
BEFORE INSERT ON automation_github_pull_request_triggers
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_pull_request'
  OR EXISTS (SELECT 1 FROM automation_github_issue_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_webhook_triggers WHERE automation_id = NEW.automation_id)
BEGIN SELECT RAISE(ABORT, 'GitHub pull-request Trigger does not match Automation type'); END;

CREATE TRIGGER automation_schedule_trigger_type_guard
BEFORE INSERT ON automation_schedule_triggers
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'schedule'
  OR EXISTS (SELECT 1 FROM automation_github_issue_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_webhook_triggers WHERE automation_id = NEW.automation_id)
BEGIN SELECT RAISE(ABORT, 'Schedule Trigger does not match Automation type'); END;

CREATE TRIGGER automation_webhook_trigger_type_guard
BEFORE INSERT ON automation_github_webhook_triggers
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_webhook'
  OR EXISTS (SELECT 1 FROM automation_github_issue_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_triggers WHERE automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_triggers WHERE automation_id = NEW.automation_id)
BEGIN SELECT RAISE(ABORT, 'GitHub webhook Trigger does not match Automation type'); END;

CREATE TRIGGER automation_webhook_trigger_automation_immutable
BEFORE UPDATE OF automation_id ON automation_github_webhook_triggers
WHEN NEW.automation_id != OLD.automation_id
BEGIN SELECT RAISE(ABORT, 'GitHub webhook Trigger Automation is immutable'); END;

CREATE TRIGGER automation_issue_occurrence_type_guard
BEFORE INSERT ON automation_github_issue_occurrences
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_issue'
  OR NOT EXISTS (SELECT 1 FROM automation_occurrences WHERE id = NEW.occurrence_id AND automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_github_webhook_occurrences WHERE occurrence_id = NEW.occurrence_id)
BEGIN SELECT RAISE(ABORT, 'GitHub issue Occurrence does not match Automation type'); END;

CREATE TRIGGER automation_pull_request_occurrence_type_guard
BEFORE INSERT ON automation_github_pull_request_occurrences
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_pull_request'
  OR NOT EXISTS (SELECT 1 FROM automation_occurrences WHERE id = NEW.occurrence_id AND automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_issue_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_github_webhook_occurrences WHERE occurrence_id = NEW.occurrence_id)
BEGIN SELECT RAISE(ABORT, 'GitHub pull-request Occurrence does not match Automation type'); END;

CREATE TRIGGER automation_schedule_occurrence_type_guard
BEFORE INSERT ON automation_schedule_occurrences
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'schedule'
  OR NOT EXISTS (SELECT 1 FROM automation_occurrences WHERE id = NEW.occurrence_id AND automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_issue_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_github_webhook_occurrences WHERE occurrence_id = NEW.occurrence_id)
BEGIN SELECT RAISE(ABORT, 'Schedule Occurrence does not match Automation type'); END;

CREATE TRIGGER automation_webhook_occurrence_type_guard
BEFORE INSERT ON automation_github_webhook_occurrences
WHEN (SELECT trigger_type FROM automations WHERE id = NEW.automation_id) != 'github_webhook'
  OR NOT EXISTS (SELECT 1 FROM automation_occurrences WHERE id = NEW.occurrence_id AND automation_id = NEW.automation_id)
  OR EXISTS (SELECT 1 FROM automation_github_issue_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_github_pull_request_occurrences WHERE occurrence_id = NEW.occurrence_id)
  OR EXISTS (SELECT 1 FROM automation_schedule_occurrences WHERE occurrence_id = NEW.occurrence_id)
BEGIN SELECT RAISE(ABORT, 'GitHub webhook Occurrence does not match Automation type'); END;

CREATE TRIGGER automation_webhook_occurrence_automation_immutable
BEFORE UPDATE OF automation_id ON automation_github_webhook_occurrences
WHEN NEW.automation_id != OLD.automation_id
BEGIN SELECT RAISE(ABORT, 'GitHub webhook Occurrence Automation is immutable'); END;
