CREATE TABLE execution_profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL UNIQUE,
    current_version INTEGER NOT NULL CHECK (current_version >= 1),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    healthy INTEGER NOT NULL CHECK (healthy IN (0, 1)),
    health_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE execution_profile_versions (
    profile_id TEXT NOT NULL REFERENCES execution_profiles(id),
    version INTEGER NOT NULL CHECK (version >= 1),
    kind TEXT NOT NULL CHECK (kind = 'fake_cloud_run'),
    runtime TEXT NOT NULL CHECK (runtime IN ('pi', 'codex', 'claude-code')),
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 28800),
    resource_class TEXT NOT NULL,
    max_concurrent INTEGER NOT NULL CHECK (max_concurrent BETWEEN 1 AND 100),
    commit_resolution_policy TEXT NOT NULL CHECK (commit_resolution_policy = 'frozen_commit'),
    fake_outcome TEXT NOT NULL CHECK (fake_outcome IN ('succeeded', 'failed', 'running')),
    fake_result TEXT NOT NULL DEFAULT '',
    fake_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (profile_id, version)
);

ALTER TABLE workers ADD COLUMN synthetic INTEGER NOT NULL DEFAULT 0 CHECK (synthetic IN (0, 1));
ALTER TABLE routines ADD COLUMN execution_profile_id TEXT REFERENCES execution_profiles(id);
ALTER TABLE work ADD COLUMN requested_execution_profile_id TEXT;
ALTER TABLE work ADD COLUMN execution_snapshot TEXT NOT NULL DEFAULT '{}'
    CHECK (json_valid(execution_snapshot) AND json_type(execution_snapshot) = 'object');

ALTER TABLE work_targets ADD COLUMN execution_profile_id TEXT NOT NULL DEFAULT 'persistent-auto';
ALTER TABLE work_targets ADD COLUMN execution_profile_version INTEGER NOT NULL DEFAULT 1 CHECK (execution_profile_version >= 1);
ALTER TABLE work_targets ADD COLUMN execution_backend TEXT NOT NULL DEFAULT 'persistent'
    CHECK (execution_backend IN ('persistent', 'fake_cloud_run'));
ALTER TABLE work_targets ADD COLUMN execution_provider TEXT NOT NULL DEFAULT 'worker';
ALTER TABLE work_targets ADD COLUMN execution_model TEXT NOT NULL DEFAULT 'worker-default';
ALTER TABLE work_targets ADD COLUMN resource_class TEXT NOT NULL DEFAULT 'worker';
ALTER TABLE work_targets ADD COLUMN commit_resolution_policy TEXT NOT NULL DEFAULT 'resolve_per_attempt'
    CHECK (commit_resolution_policy IN ('resolve_per_attempt', 'frozen_commit'));

UPDATE work
SET execution_snapshot = json_object(
    'profile_id', 'persistent-auto',
    'profile_version', 1,
    'backend', 'persistent',
    'runtime', COALESCE(json_extract(routine_snapshot, '$.runtime'), 'codex'),
    'provider', 'worker',
    'model', 'worker-default',
    'timeout_seconds', COALESCE(
        json_extract(routine_snapshot, '$.timeout_seconds'),
        (SELECT timeout_seconds FROM work_targets target WHERE target.work_id = work.id LIMIT 1),
        7200
    ),
    'resource_class', 'worker',
    'commit_resolution_policy', 'resolve_per_attempt'
);

CREATE INDEX execution_profiles_name ON execution_profiles(name_key);
CREATE INDEX execution_profile_versions_profile ON execution_profile_versions(profile_id, version DESC);
CREATE INDEX work_targets_backend_claim ON work_targets(execution_backend, state, admitted_at, id);
