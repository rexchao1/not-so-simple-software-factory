CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

CREATE TABLE workers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    worker_version TEXT NOT NULL,
    codex_version TEXT NOT NULL,
    capacity INTEGER NOT NULL CHECK (capacity BETWEEN 1 AND 100),
    active_count INTEGER NOT NULL CHECK (active_count >= 0),
    health TEXT NOT NULL CHECK (health IN ('healthy', 'unhealthy')),
    retained_worktrees_json TEXT NOT NULL DEFAULT '[]',
    registered_at INTEGER NOT NULL,
    last_heartbeat INTEGER NOT NULL
);

CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    remote_identity TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

CREATE TABLE worker_repositories (
    worker_id TEXT NOT NULL REFERENCES workers(id),
    display_key TEXT NOT NULL,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    retained_count INTEGER NOT NULL DEFAULT 0 CHECK (retained_count >= 0),
    advertised INTEGER NOT NULL DEFAULT 1 CHECK (advertised IN (0, 1)),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (worker_id, display_key),
    UNIQUE (worker_id, repository_id)
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    description TEXT NOT NULL,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    timeout_seconds INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE executions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),
    assigned_worker_id TEXT NOT NULL REFERENCES workers(id),
    required_runtime TEXT NOT NULL CHECK (required_runtime = 'codex'),
    state TEXT NOT NULL CHECK (state IN ('queued', 'preparing', 'running', 'succeeded', 'failed', 'cancelled')),
    cancellation_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancellation_requested IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX executions_claim_order
ON executions(assigned_worker_id, state, created_at, id);

CREATE TABLE attempts (
    id TEXT PRIMARY KEY,
    execution_id TEXT NOT NULL REFERENCES executions(id),
    worker_id TEXT NOT NULL REFERENCES workers(id),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    state TEXT NOT NULL CHECK (state IN ('preparing', 'running', 'succeeded', 'failed', 'cancelled', 'lost')),
    lease_digest BLOB NOT NULL,
    lease_expires_at INTEGER NOT NULL,
    supervisor_pid INTEGER,
    process_identity TEXT,
    process_group_id INTEGER,
    result TEXT,
    error TEXT,
    started_at INTEGER,
    completed_at INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (execution_id, attempt_number)
);

CREATE UNIQUE INDEX one_active_attempt_per_execution
ON attempts(execution_id)
WHERE state IN ('preparing', 'running');

CREATE INDEX attempts_expiry
ON attempts(state, lease_expires_at);

CREATE TABLE claim_requests (
    worker_id TEXT NOT NULL REFERENCES workers(id),
    request_id TEXT NOT NULL,
    lease_digest BLOB NOT NULL,
    attempt_id TEXT REFERENCES attempts(id),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (worker_id, request_id)
);

CREATE TABLE attempt_events (
    attempt_id TEXT NOT NULL REFERENCES attempts(id),
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    kind TEXT NOT NULL,
    payload BLOB NOT NULL,
    payload_bytes INTEGER NOT NULL,
    server_time INTEGER NOT NULL,
    PRIMARY KEY (attempt_id, sequence)
);
