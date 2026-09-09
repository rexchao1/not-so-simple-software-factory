CREATE TABLE workflows (
    id TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    current_revision_id TEXT NOT NULL UNIQUE
        REFERENCES workflow_revisions(id) DEFERRABLE INITIALLY DEFERRED,
    current_name_key TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE workflow_revisions (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id),
    revision_number INTEGER NOT NULL CHECK (revision_number BETWEEN 1 AND 100),
    request_key TEXT NOT NULL UNIQUE,
    request_digest BLOB NOT NULL,
    name TEXT NOT NULL,
    summary TEXT NOT NULL,
    instructions TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (workflow_id, revision_number)
);

CREATE INDEX workflow_revisions_history
ON workflow_revisions(workflow_id, revision_number DESC);

ALTER TABLE tasks ADD COLUMN workflow_id TEXT REFERENCES workflows(id);
ALTER TABLE tasks ADD COLUMN workflow_revision_id TEXT REFERENCES workflow_revisions(id);
ALTER TABLE tasks ADD COLUMN workflow_name TEXT;
ALTER TABLE tasks ADD COLUMN workflow_revision_number INTEGER;
ALTER TABLE tasks ADD COLUMN context TEXT;

UPDATE tasks SET context = description;

CREATE INDEX workflows_list_order ON workflows(updated_at DESC, id DESC);
