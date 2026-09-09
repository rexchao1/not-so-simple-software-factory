CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    request_digest BLOB NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('manual')),
    definition_id TEXT NOT NULL REFERENCES definitions(id),
    definition_snapshot TEXT NOT NULL
        CHECK (json_valid(definition_snapshot) AND json_type(definition_snapshot) = 'object'),
    parameters TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(parameters) AND json_type(parameters) = 'object'),
    admitted_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    task_id TEXT UNIQUE REFERENCES tasks(id),
    execution_id TEXT UNIQUE REFERENCES executions(id),
    state TEXT NOT NULL CHECK (state IN ('blocked', 'queued', 'cancelled')),
    blocked_reason TEXT,
    admitted_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (run_id, repository_id)
);

CREATE INDEX runs_list_order ON runs(admitted_at DESC, id DESC);
CREATE INDEX jobs_run_order ON jobs(run_id, admitted_at, id);
CREATE INDEX jobs_blocked_order ON jobs(state, admitted_at, id);
