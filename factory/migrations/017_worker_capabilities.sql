-- factory: foreign-keys-off

-- Keep the legacy primary runtime columns for older workers and clients while
-- adding the capability list used by multi-runtime local Workers.
CREATE TABLE workers_v17 (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    worker_version TEXT NOT NULL,
    runtime_version TEXT NOT NULL,
    capacity INTEGER NOT NULL CHECK (capacity BETWEEN 1 AND 100),
    active_count INTEGER NOT NULL CHECK (active_count >= 0),
    health TEXT NOT NULL CHECK (health IN ('healthy', 'unhealthy')),
    retained_worktrees_json TEXT NOT NULL DEFAULT '[]',
    registered_at INTEGER NOT NULL,
    last_heartbeat INTEGER NOT NULL,
    runtime TEXT NOT NULL DEFAULT 'codex'
        CHECK (runtime IN ('pi', 'codex', 'claude-code')),
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    source_access_json TEXT NOT NULL DEFAULT '[]',
    accepts_managed_repositories INTEGER NOT NULL DEFAULT 0
        CHECK (accepts_managed_repositories IN (0, 1)),
    managed_repository_ids_json TEXT NOT NULL DEFAULT '[]',
    weekly_limit_used_percent INTEGER
        CHECK (weekly_limit_used_percent BETWEEN 0 AND 100),
    weekly_limit_resets_at INTEGER
);

INSERT INTO workers_v17(
    id, name, worker_version, runtime_version, capacity, active_count, health,
    retained_worktrees_json, registered_at, last_heartbeat, runtime,
    capabilities_json, source_access_json, accepts_managed_repositories,
    managed_repository_ids_json, weekly_limit_used_percent, weekly_limit_resets_at
)
SELECT
    id, name, worker_version, runtime_version, capacity, active_count, health,
    retained_worktrees_json, registered_at, last_heartbeat, runtime,
    json_array(json_object(
        'kind', 'runtime',
        'name', runtime,
        'status', CASE health WHEN 'healthy' THEN 'ready' ELSE 'unhealthy' END,
        'version', runtime_version
    )),
    source_access_json, accepts_managed_repositories, managed_repository_ids_json,
    weekly_limit_used_percent, weekly_limit_resets_at
FROM workers;

DROP TABLE workers;
ALTER TABLE workers_v17 RENAME TO workers;

-- Pi Jobs use the same execution and attempt lifecycle as the existing
-- runtimes. Rebuild the table to widen its CHECK constraint without losing
-- queued or historical work.
CREATE TABLE executions_v17 (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),
    assigned_worker_id TEXT NOT NULL REFERENCES workers(id),
    required_runtime TEXT NOT NULL CHECK (required_runtime IN ('pi', 'codex', 'claude-code')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'preparing', 'running', 'succeeded', 'failed', 'cancelled')),
    cancellation_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancellation_requested IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0)
);

INSERT INTO executions_v17(
    id, task_id, assigned_worker_id, required_runtime, state,
    cancellation_requested, created_at, updated_at, retry_count
)
SELECT
    id, task_id, assigned_worker_id, required_runtime, state,
    cancellation_requested, created_at, updated_at, retry_count
FROM executions;

DROP TABLE executions;
ALTER TABLE executions_v17 RENAME TO executions;

CREATE INDEX executions_claim_order
ON executions(assigned_worker_id, state, created_at, id);

CREATE INDEX executions_metrics_created
ON executions(created_at);

CREATE INDEX executions_metrics_outcomes
ON executions(state, updated_at);
