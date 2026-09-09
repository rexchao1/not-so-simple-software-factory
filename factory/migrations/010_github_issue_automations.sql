CREATE TABLE automations (
    id TEXT PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    request_digest BLOB NOT NULL,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL UNIQUE,
    workflow_id TEXT NOT NULL REFERENCES workflows(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    context TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 28800),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    trigger_type TEXT NOT NULL CHECK (trigger_type = 'github_issue'),
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

CREATE TABLE automation_github_issue_triggers (
    automation_id TEXT PRIMARY KEY REFERENCES automations(id),
    issue_state TEXT NOT NULL CHECK (issue_state IN ('open', 'closed')),
    required_labels_json TEXT NOT NULL,
    poll_interval_seconds INTEGER NOT NULL CHECK (poll_interval_seconds BETWEEN 10 AND 86400)
);

CREATE TABLE automation_occurrences (
    id TEXT PRIMARY KEY,
    automation_id TEXT NOT NULL REFERENCES automations(id),
    automation_version INTEGER NOT NULL,
    automation_name TEXT NOT NULL,
    workflow_revision_id TEXT NOT NULL REFERENCES workflow_revisions(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    repository_identity TEXT NOT NULL,
    context TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'dispatched', 'failed', 'task_deleted')),
    resolved_prompt TEXT,
    task_request_key TEXT NOT NULL UNIQUE,
    task_id TEXT UNIQUE REFERENCES tasks(id) ON DELETE RESTRICT,
    task_id_snapshot TEXT NOT NULL DEFAULT '',
    diagnostic TEXT NOT NULL DEFAULT '',
    retry_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE automation_github_issue_occurrences (
    occurrence_id TEXT PRIMARY KEY REFERENCES automation_occurrences(id),
    automation_id TEXT NOT NULL REFERENCES automations(id),
    issue_number INTEGER NOT NULL CHECK (issue_number > 0),
    issue_url TEXT NOT NULL,
    issue_title TEXT NOT NULL,
    observed_state TEXT NOT NULL CHECK (observed_state IN ('open', 'closed')),
    observed_labels_json TEXT NOT NULL,
    configured_state TEXT NOT NULL CHECK (configured_state IN ('open', 'closed')),
    required_labels_json TEXT NOT NULL,
    UNIQUE (automation_id, issue_number)
);

CREATE INDEX automations_due
ON automations(enabled, next_check_at, id);

CREATE INDEX automation_occurrences_pending
ON automation_occurrences(state, retry_at, created_at, id);

CREATE INDEX automation_occurrences_history
ON automation_occurrences(automation_id, created_at DESC, id DESC);
