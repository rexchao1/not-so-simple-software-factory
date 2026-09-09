-- factory: foreign-keys-off

-- Migration 35 adds the draft state ahead of queued. SQLite cannot alter a
-- CHECK constraint in place, so sessions and runs are rebuilt the same way
-- migration 031 rebuilt sessions. Draft satisfies every existing compound
-- CHECK without change: execution_owner stays 'none', the state is not
-- needs-input or ready, and it is not terminal.

CREATE TABLE sessions_draft_admission (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES "runs"(id) ON DELETE CASCADE,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    repository_identity TEXT NOT NULL,
    resolved_prompt TEXT NOT NULL,
    required_runtime TEXT NOT NULL CHECK (required_runtime IN ('pi', 'codex', 'claude-code')),
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 28800),
    state TEXT NOT NULL CHECK (state IN (
        'draft', 'blocked', 'queued', 'preparing', 'running', 'needs-input',
        'ready', 'succeeded', 'failed', 'no-change', 'cancelled'
    )),
    blocked_reason TEXT,
    assigned_worker_id TEXT REFERENCES workers(id),
    cancellation_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancellation_requested IN (0, 1)),
    retry_may_repeat_effects INTEGER NOT NULL DEFAULT 0 CHECK (retry_may_repeat_effects IN (0, 1)),
    admitted_at INTEGER NOT NULL,
    started_at INTEGER,
    terminal_at INTEGER,
    result TEXT,
    failure_reason TEXT,
    execution_profile_id TEXT NOT NULL DEFAULT 'persistent-auto',
    execution_profile_version INTEGER NOT NULL DEFAULT 1 CHECK (execution_profile_version >= 1),
    execution_backend TEXT NOT NULL DEFAULT 'persistent'
        CHECK (execution_backend IN ('persistent', 'fake_cloud_run')),
    execution_provider TEXT NOT NULL DEFAULT 'worker',
    execution_model TEXT NOT NULL DEFAULT 'worker-default',
    resource_class TEXT NOT NULL DEFAULT 'worker',
    commit_resolution_policy TEXT NOT NULL DEFAULT 'resolve_per_attempt'
        CHECK (commit_resolution_policy IN ('resolve_per_attempt', 'frozen_commit')),
    target_position INTEGER NOT NULL DEFAULT 0 CHECK (target_position BETWEEN 0 AND 99),
    target_key TEXT NOT NULL DEFAULT '' CHECK (length(CAST(target_key AS BLOB)) <= 1024),
    target_kind TEXT NOT NULL DEFAULT 'repository'
        CHECK (target_kind IN ('work_item', 'repository')),
    source_kind TEXT NOT NULL DEFAULT 'repository'
        CHECK (source_kind IN ('github_issue', 'opaque', 'repository')),
    source_key TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_key AS BLOB)) <= 1024),
    source_reference TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_reference AS BLOB)) <= 2048),
    context_snapshot TEXT NOT NULL DEFAULT '' CHECK (length(CAST(context_snapshot AS BLOB)) <= 65536),
    publish_branch TEXT NOT NULL DEFAULT '' CHECK (length(CAST(publish_branch AS BLOB)) <= 255),
    predecessor_work_id TEXT REFERENCES sessions_draft_admission(id),
    execution_owner TEXT NOT NULL DEFAULT 'none'
        CHECK (execution_owner IN ('none', 'worker_attempt', 'operator')),
    waiting_reason TEXT NOT NULL DEFAULT '' CHECK (length(CAST(waiting_reason AS BLOB)) <= 2048),
    latest_progress TEXT NOT NULL DEFAULT '' CHECK (length(CAST(latest_progress AS BLOB)) <= 2048),
    question TEXT NOT NULL DEFAULT '' CHECK (length(CAST(question AS BLOB)) <= 8192),
    checkpoint_sha TEXT NOT NULL DEFAULT '' CHECK (length(checkpoint_sha) <= 64),
    pending_resume_sha TEXT NOT NULL DEFAULT '' CHECK (length(pending_resume_sha) <= 64),
    pull_request_url TEXT NOT NULL DEFAULT '' CHECK (length(CAST(pull_request_url AS BLOB)) <= 2048),
    pull_request_head_branch TEXT NOT NULL DEFAULT '' CHECK (length(CAST(pull_request_head_branch AS BLOB)) <= 255),
    pull_request_head_sha TEXT NOT NULL DEFAULT '' CHECK (length(pull_request_head_sha) <= 64),
    terminal_message TEXT NOT NULL DEFAULT '' CHECK (length(CAST(terminal_message AS BLOB)) <= 8192),
    answer TEXT NOT NULL DEFAULT '' CHECK (length(CAST(answer AS BLOB)) <= 8192),
    checkpoint_published INTEGER NOT NULL DEFAULT 0 CHECK (checkpoint_published IN (0, 1)),
    approved_by TEXT NOT NULL DEFAULT '' CHECK (length(CAST(approved_by AS BLOB)) <= 255),
    approved_at INTEGER,
    delivery TEXT NOT NULL DEFAULT 'pr'
        CHECK (delivery IN ('pr', 'pr+automerge', 'branch')),
    CHECK (
        (state = 'preparing' AND execution_owner = 'worker_attempt')
        OR (state = 'running' AND execution_owner IN ('worker_attempt', 'operator'))
        OR (state NOT IN ('preparing', 'running') AND execution_owner = 'none')
    ),
    CHECK (state != 'needs-input' OR (question != '' AND checkpoint_sha != '' AND pending_resume_sha != '')),
    CHECK (state != 'ready' OR pull_request_url != ''),
    CHECK (state NOT IN ('ready', 'succeeded', 'failed', 'no-change', 'cancelled') OR terminal_at IS NOT NULL),
    CHECK (state != 'draft' OR (approved_at IS NULL AND approved_by = ''))
);

INSERT INTO sessions_draft_admission(
    id, run_id, repository_id, repository_identity, resolved_prompt,
    required_runtime, timeout_seconds, state, blocked_reason, assigned_worker_id,
    cancellation_requested, retry_may_repeat_effects, admitted_at, started_at,
    terminal_at, result, failure_reason, execution_profile_id,
    execution_profile_version, execution_backend, execution_provider,
    execution_model, resource_class, commit_resolution_policy, target_position,
    target_key, target_kind, source_kind, source_key, source_reference,
    context_snapshot, publish_branch, predecessor_work_id, execution_owner,
    waiting_reason, latest_progress, question, checkpoint_sha,
    pending_resume_sha, pull_request_url, pull_request_head_branch,
    pull_request_head_sha, terminal_message, answer, checkpoint_published
)
SELECT
    id, run_id, repository_id, repository_identity, resolved_prompt,
    required_runtime, timeout_seconds, state, blocked_reason, assigned_worker_id,
    cancellation_requested, retry_may_repeat_effects, admitted_at, started_at,
    terminal_at, result, failure_reason, execution_profile_id,
    execution_profile_version, execution_backend, execution_provider,
    execution_model, resource_class, commit_resolution_policy, target_position,
    target_key, target_kind, source_kind, source_key, source_reference,
    context_snapshot, publish_branch, predecessor_work_id, execution_owner,
    waiting_reason, latest_progress, question, checkpoint_sha,
    pending_resume_sha, pull_request_url, pull_request_head_branch,
    pull_request_head_sha, terminal_message, answer, checkpoint_published
FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_draft_admission RENAME TO sessions;

CREATE INDEX sessions_run_order ON sessions(run_id, target_position, id);
CREATE INDEX sessions_claim_order ON sessions(state, admitted_at, id);
CREATE INDEX sessions_backend_claim ON sessions(execution_backend, state, admitted_at, id);
CREATE INDEX sessions_predecessor ON sessions(predecessor_work_id);
CREATE UNIQUE INDEX sessions_run_target_key
    ON sessions(run_id, target_key) WHERE target_key != '';
CREATE UNIQUE INDEX sessions_run_target_position
    ON sessions(run_id, target_position) WHERE target_key != '';
CREATE INDEX sessions_retry_identity
    ON sessions(repository_id, source_kind, source_key, state);

-- runs carries the admission provenance. source already exists with three
-- values and is extended rather than duplicated.

CREATE TABLE runs_draft_admission (
    id TEXT PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    request_digest BLOB NOT NULL,
    task_id TEXT NOT NULL REFERENCES "tasks"(id),
    task_snapshot TEXT NOT NULL
        CHECK (json_valid(task_snapshot) AND json_type(task_snapshot) = 'object'),
    source TEXT NOT NULL CHECK (source IN (
        'manual', 'schedule', 'provider_history', 'orchestrator', 'cockpit', 'github'
    )),
    scheduled_at INTEGER,
    provider_snapshot TEXT
        CHECK (provider_snapshot IS NULL OR (
            json_valid(provider_snapshot) AND json_type(provider_snapshot) = 'object'
        )),
    admitted_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    terminal_at INTEGER,
    requested_execution_profile_id TEXT,
    execution_snapshot TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(execution_snapshot) AND json_type(execution_snapshot) = 'object'),
    outcome_contract TEXT NOT NULL DEFAULT 'process_exit'
        CHECK (outcome_contract IN ('process_exit', 'agent_update')),
    targets_snapshot TEXT NOT NULL DEFAULT '[]'
        CHECK (json_valid(targets_snapshot) AND json_type(targets_snapshot) = 'array'),
    pre_approved INTEGER NOT NULL DEFAULT 0 CHECK (pre_approved IN (0, 1)),
    CHECK (pre_approved = 0 OR source = 'orchestrator')
);

INSERT INTO runs_draft_admission(
    id, request_key, request_digest, task_id, task_snapshot, source,
    scheduled_at, provider_snapshot, admitted_at, updated_at, terminal_at,
    requested_execution_profile_id, execution_snapshot, outcome_contract,
    targets_snapshot
)
SELECT
    id, request_key, request_digest, task_id, task_snapshot, source,
    scheduled_at, provider_snapshot, admitted_at, updated_at, terminal_at,
    requested_execution_profile_id, execution_snapshot, outcome_contract,
    targets_snapshot
FROM runs;

DROP TABLE runs;
ALTER TABLE runs_draft_admission RENAME TO runs;

CREATE INDEX runs_list_order ON runs(admitted_at DESC, id DESC);

ALTER TABLE repositories ADD COLUMN default_delivery TEXT NOT NULL DEFAULT 'pr'
    CHECK (default_delivery IN ('pr', 'pr+automerge', 'branch'));
