CREATE TABLE definitions (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL UNIQUE,
    prompt TEXT NOT NULL,
    runtime TEXT NOT NULL CHECK (runtime IN ('pi', 'codex', 'claude-code')),
    allowed_tools TEXT NOT NULL CHECK (json_valid(allowed_tools) AND json_type(allowed_tools) = 'array'),
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 28800),
    inputs TEXT NOT NULL CHECK (json_valid(inputs) AND json_type(inputs) = 'object'),
    generation INTEGER NOT NULL CHECK (generation >= 1),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE definition_mutations (
    request_key TEXT PRIMARY KEY,
    request_digest BLOB NOT NULL,
    definition_id TEXT NOT NULL REFERENCES definitions(id),
    created_at INTEGER NOT NULL
);

CREATE INDEX definitions_list_order ON definitions(archived, updated_at DESC, id DESC);

ALTER TABLE tasks ADD COLUMN definition_id TEXT REFERENCES definitions(id);
ALTER TABLE tasks ADD COLUMN definition_snapshot TEXT
    CHECK (definition_snapshot IS NULL OR (
        json_valid(definition_snapshot) AND json_type(definition_snapshot) = 'object'
    ));
