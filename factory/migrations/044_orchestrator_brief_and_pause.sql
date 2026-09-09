-- factory: foreign-keys-off

-- Optional concise metadata authored by the trusted orchestrator at admission.
ALTER TABLE runs ADD COLUMN orchestrator_brief TEXT NOT NULL DEFAULT '';

-- A single durable switch stops both admission and dispatch without affecting
-- attempts which have already started.
ALTER TABLE factory_settings ADD COLUMN paused INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1));
ALTER TABLE factory_settings ADD COLUMN paused_at INTEGER;
