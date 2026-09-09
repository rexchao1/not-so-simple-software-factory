-- factory: foreign-keys-off
--
-- A worker has one immutable runtime. Existing workers were Codex-only, so the
-- migration records that runtime explicitly and preserves their version.
ALTER TABLE workers
ADD COLUMN runtime TEXT NOT NULL DEFAULT 'codex'
CHECK (runtime IN ('codex', 'claude-code'));

ALTER TABLE workers
RENAME COLUMN codex_version TO runtime_version;

-- SQLite cannot widen a CHECK constraint in place. Rebuild executions while
-- retaining IDs so attempts and claim requests continue to reference them.
CREATE TABLE executions_v4 (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),
    assigned_worker_id TEXT NOT NULL REFERENCES workers(id),
    required_runtime TEXT NOT NULL CHECK (required_runtime IN ('codex', 'claude-code')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'preparing', 'running', 'succeeded', 'failed', 'cancelled')),
    cancellation_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancellation_requested IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

INSERT INTO executions_v4(
    id, task_id, assigned_worker_id, required_runtime, state,
    cancellation_requested, created_at, updated_at
)
SELECT
    id, task_id, assigned_worker_id, required_runtime, state,
    cancellation_requested, created_at, updated_at
FROM executions;

DROP TABLE executions;
ALTER TABLE executions_v4 RENAME TO executions;

CREATE INDEX executions_claim_order
ON executions(assigned_worker_id, state, created_at, id);
