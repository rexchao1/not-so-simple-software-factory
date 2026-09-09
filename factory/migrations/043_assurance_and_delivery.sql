-- factory: foreign-keys-off

-- Migration 43 records the assurance decision made at admission and adds a
-- model-free delivery stage. Fast is an explicit opt-in; every existing Run is
-- reviewed. Delivery has neither a prompt nor a command because the Worker
-- owns its fixed git and GitHub operations.

ALTER TABLE runs ADD COLUMN assurance TEXT NOT NULL DEFAULT 'reviewed'
    CHECK (assurance IN ('reviewed', 'fast'));

CREATE TABLE pipeline_stages_delivery (
    pipeline_id TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 19),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 200),
    kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'code', 'delivery')),
    prompt TEXT NOT NULL DEFAULT '',
    command TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    effort TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (pipeline_id, position),
    CHECK (
        (kind = 'agent'
            AND length(CAST(prompt AS BLOB)) BETWEEN 1 AND 65536
            AND command = '')
        OR (kind = 'code'
            AND prompt = ''
            AND length(CAST(command AS BLOB)) BETWEEN 1 AND 4096
            AND model = '' AND effort = '')
        OR (kind = 'delivery'
            AND prompt = '' AND command = ''
            AND model = '' AND effort = '')
    )
);

INSERT INTO pipeline_stages_delivery(
    pipeline_id, position, name, kind, prompt, command, model, effort
)
SELECT pipeline_id, position, name, kind, prompt, command, model, effort
FROM pipeline_stages;
DROP TABLE pipeline_stages;
ALTER TABLE pipeline_stages_delivery RENAME TO pipeline_stages;

-- The fast preset is one model context followed by deterministic delivery.
-- Its stable ID makes orchestrator submission independent of operator-created
-- naming, while the ordinary Single agent pipeline remains unchanged.
INSERT INTO pipelines(id, name, name_key, generation, created_at, updated_at)
VALUES ('00000000-0000-0000-0000-000000000002', 'Fast', 'fast', 1, 0, 0);
INSERT INTO pipeline_stages(pipeline_id, position, name, kind, prompt, command, model, effort)
VALUES
    ('00000000-0000-0000-0000-000000000002', 0, 'Implement', 'agent',
     'Implement and commit the task. Run focused existing checks that add useful signal. Do not run the full suite unless the task explicitly requires it. Do not push, open a pull request, or call factory update; Factory delivers the committed work.

{{ task.prompt }}',
     '', 'sonnet', 'medium'),
    ('00000000-0000-0000-0000-000000000002', 1, 'Deliver', 'delivery', '', '', '', '');

CREATE TABLE session_stages_delivery (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 19),
    name TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'code', 'delivery')),
    prompt TEXT NOT NULL DEFAULT '',
    command TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at INTEGER,
    completed_at INTEGER,
    review_verdict TEXT NOT NULL DEFAULT ''
        CHECK (review_verdict IN ('', 'approve', 'request-changes', 'blocked')),
    model TEXT NOT NULL DEFAULT '',
    effort TEXT NOT NULL DEFAULT '',
    cost_usd REAL CHECK (cost_usd IS NULL OR cost_usd >= 0),
    input_tokens INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
    cache_creation_input_tokens INTEGER CHECK (cache_creation_input_tokens IS NULL OR cache_creation_input_tokens >= 0),
    cache_read_input_tokens INTEGER CHECK (cache_read_input_tokens IS NULL OR cache_read_input_tokens >= 0),
    output_tokens INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
    models TEXT CHECK (models IS NULL OR json_valid(models)),
    PRIMARY KEY (session_id, position),
    CHECK (
        (kind = 'agent' AND command = '')
        OR (kind = 'code' AND prompt = '' AND length(CAST(command AS BLOB)) BETWEEN 1 AND 4096
            AND model = '' AND effort = '')
        OR (kind = 'delivery' AND prompt = '' AND command = '' AND model = '' AND effort = '')
    )
);

INSERT INTO session_stages_delivery(
    session_id, position, name, kind, prompt, command, state, result, error,
    started_at, completed_at, review_verdict, model, effort,
    cost_usd, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, models
)
SELECT session_id, position, name, kind, prompt, command, state, result, error,
       started_at, completed_at, review_verdict, model, effort,
       cost_usd, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, models
FROM session_stages;
DROP TABLE session_stages;
ALTER TABLE session_stages_delivery RENAME TO session_stages;
CREATE INDEX session_stages_state ON session_stages(session_id, state, position);
