--
-- 042: per-stage model and effort.
--
-- Four things.
--
-- 1. pipeline_stages.model and .effort. The Pipeline is where a stage's
--    execution is described, next to the prompt that describes its work.
--    Both default to the empty string, which means "inherit", so every
--    Pipeline that exists before this migration keeps behaving exactly as it
--    did. AC-11 is that property.
--
-- 2. session_stages.model and .effort. These are the RESOLVED values, frozen
--    at admission the way the sandbox posture is frozen. Freezing is what
--    makes a Run explainable after the fact: until now every Run recorded
--    model 'worker-default', so two runs of one Pipeline that behaved
--    differently could not be told apart. INV-13 is that property.
--
-- 3. factory_settings. One row, enforced by a CHECK on a fixed primary key.
--    This is the global floor of the precedence chain.
--
--    The obvious home for a global default was the execution profile, which
--    already carries a model column. That does not work, and the reason is
--    recorded here so it is not rediscovered. persistent-auto is the profile
--    every Task uses unless one is named, and it is not a row at all:
--    tasks.go synthesizes it inline. It is deliberately read-only, and
--    normalizeExecutionProfile rejects any kind that is not fake_cloud_run or
--    docker, so no replacement can be created. The execution profile's model
--    column can therefore only be set on profiles that runs on this host
--    never touch.
--
-- 4. run_stage_overrides. A per-run, per-position override, set on a draft
--    before it is approved. Keyed on run_id rather than on the Task, because
--    the override applies to one admission and must not leak into the next
--    run of the same Task. ON DELETE CASCADE, because an override has no
--    meaning without its run.
--
-- No CHECK constrains the values to the supported lists. SQLite cannot alter a
-- CHECK in place, so a new model alias would need a full table rebuild. The
-- lists live in internal/protocol/stage_execution.go and are enforced at save
-- time under INV-12, which is where a clear error message is possible.
--

ALTER TABLE pipeline_stages ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE pipeline_stages ADD COLUMN effort TEXT NOT NULL DEFAULT '';

ALTER TABLE session_stages ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE session_stages ADD COLUMN effort TEXT NOT NULL DEFAULT '';

CREATE TABLE factory_settings (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    default_model  TEXT NOT NULL DEFAULT '',
    default_effort TEXT NOT NULL DEFAULT '',
    updated_at     INTEGER NOT NULL
);

INSERT INTO factory_settings(id, default_model, default_effort, updated_at)
VALUES (1, '', '', 0);

CREATE TABLE run_stage_overrides (
    run_id   TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    model    TEXT NOT NULL DEFAULT '',
    effort   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, position)
);
