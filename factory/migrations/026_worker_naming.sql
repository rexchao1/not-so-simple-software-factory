CREATE TABLE worker_enrollments (
    id TEXT PRIMARY KEY,
    worker_id TEXT NOT NULL,
    token_digest BLOB NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    used_at INTEGER,
    created_at INTEGER NOT NULL
);

INSERT INTO worker_enrollments(
    id, worker_id, token_digest, expires_at, used_at, created_at
)
SELECT id, worker_id, token_digest, expires_at, used_at, created_at
FROM runner_enrollments;

CREATE INDEX worker_enrollments_expiry
ON worker_enrollments(expires_at);

CREATE TABLE remote_worker_credentials (
    worker_id TEXT PRIMARY KEY,
    token_digest BLOB NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL
);

INSERT INTO remote_worker_credentials(
    worker_id, token_digest, created_at, last_used_at
)
SELECT worker_id, token_digest, created_at, last_used_at
FROM remote_runner_credentials;

DROP TABLE remote_runner_credentials;
DROP TABLE runner_enrollments;

UPDATE jobs
SET blocked_reason = 'Waiting for a healthy compatible Worker with repository access.'
WHERE blocked_reason = 'Waiting for a healthy compatible Runner with repository access.';
