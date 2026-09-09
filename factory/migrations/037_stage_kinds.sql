-- factory: foreign-keys-off

-- Migration 37 gives a Pipeline stage a kind. An agent stage carries a prompt
-- and spawns a runtime, exactly as every stage does today. A code stage
-- carries a command instead, runs it in the worktree, and never invokes a
-- model, which is what INV-7 requires.
--
-- Both stage tables need a rebuild rather than an ALTER, because both constrain
-- prompt to at least one byte and a code stage has no prompt. SQLite cannot
-- alter a CHECK in place. The rebuild follows migrations/031 and 035: create,
-- copy, drop, rename, recreate indexes.
--
-- The paired CHECK on each table is the point of the migration. It makes "an
-- agent stage has a prompt and no command, a code stage has a command and no
-- prompt" a storage invariant rather than a rule the control plane remembers.

CREATE TABLE pipeline_stages_stage_kinds (
    pipeline_id TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 19),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 200),
    kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'code')),
    prompt TEXT NOT NULL DEFAULT '',
    command TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (pipeline_id, position),
    CHECK (
        (kind = 'agent'
            AND length(CAST(prompt AS BLOB)) BETWEEN 1 AND 65536
            AND command = '')
        OR (kind = 'code'
            AND prompt = ''
            AND length(CAST(command AS BLOB)) BETWEEN 1 AND 4096)
    )
);

INSERT INTO pipeline_stages_stage_kinds(pipeline_id, position, name, kind, prompt, command)
SELECT pipeline_id, position, name, 'agent', prompt, ''
FROM pipeline_stages;

DROP TABLE pipeline_stages;
ALTER TABLE pipeline_stages_stage_kinds RENAME TO pipeline_stages;

CREATE TABLE session_stages_stage_kinds (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 19),
    name TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'code')),
    prompt TEXT NOT NULL DEFAULT '',
    command TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at INTEGER,
    completed_at INTEGER,
    PRIMARY KEY (session_id, position),
    CHECK (
        (kind = 'agent' AND command = '')
        OR (kind = 'code'
            AND prompt = ''
            AND length(CAST(command AS BLOB)) BETWEEN 1 AND 4096)
    )
);

-- A frozen agent stage keeps whatever prompt it was admitted with, including
-- the empty prompt an older compatibility Session may carry, so this copy must
-- not impose the pipeline table's lower bound on historical rows.
INSERT INTO session_stages_stage_kinds(
    session_id, position, name, kind, prompt, command,
    state, result, error, started_at, completed_at
)
SELECT session_id, position, name, 'agent', prompt, '',
       state, result, error, started_at, completed_at
FROM session_stages;

DROP TABLE session_stages;
ALTER TABLE session_stages_stage_kinds RENAME TO session_stages;

CREATE INDEX session_stages_state ON session_stages(session_id, state, position);
