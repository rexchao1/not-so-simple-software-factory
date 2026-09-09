-- factory: foreign-keys-off

-- SQLite cannot widen a CHECK constraint in place. Rebuild workers while
-- retaining IDs so every referencing table continues to point at the same
-- worker after an upgrade from the previous 1..4 capacity range.
CREATE TABLE workers_v16 (
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
        CHECK (runtime IN ('codex', 'claude-code')),
    source_access_json TEXT NOT NULL DEFAULT '[]',
    accepts_managed_repositories INTEGER NOT NULL DEFAULT 0
        CHECK (accepts_managed_repositories IN (0, 1)),
    managed_repository_ids_json TEXT NOT NULL DEFAULT '[]',
    weekly_limit_used_percent INTEGER
        CHECK (weekly_limit_used_percent BETWEEN 0 AND 100),
    weekly_limit_resets_at INTEGER
);

INSERT INTO workers_v16(
    id, name, worker_version, runtime_version, capacity, active_count, health,
    retained_worktrees_json, registered_at, last_heartbeat, runtime,
    source_access_json, accepts_managed_repositories, managed_repository_ids_json,
    weekly_limit_used_percent, weekly_limit_resets_at
)
SELECT
    id, name, worker_version, runtime_version, capacity, active_count, health,
    retained_worktrees_json, registered_at, last_heartbeat, runtime,
    source_access_json, accepts_managed_repositories, managed_repository_ids_json,
    weekly_limit_used_percent, weekly_limit_resets_at
FROM workers;

DROP TABLE workers;
ALTER TABLE workers_v16 RENAME TO workers;
