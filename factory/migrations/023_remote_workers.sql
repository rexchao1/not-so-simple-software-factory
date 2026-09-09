ALTER TABLE workers ADD COLUMN labels_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE runner_enrollments (
    id TEXT PRIMARY KEY,
    worker_id TEXT NOT NULL,
    token_digest BLOB NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    used_at INTEGER,
    created_at INTEGER NOT NULL
);

CREATE INDEX runner_enrollments_expiry
ON runner_enrollments(expires_at);

CREATE TABLE remote_runner_credentials (
    worker_id TEXT PRIMARY KEY,
    token_digest BLOB NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL
);
