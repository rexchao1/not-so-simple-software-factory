-- 046: planning state moves into the factory.
--
-- Routes, checkpoint PRDs, review answers, boulders and pebbles were markdown
-- and JSON files in the orchestrator's own gitignored directory, which the
-- cockpit read through roadmap_root and nothing wrote through an API. Two
-- machines editing that directory was the source of the sync pain, so the
-- state moves here and the files go away. The reader that parsed them becomes
-- a reader of these tables and keeps returning exactly the same JSON.
--
-- Four tables, because the shape has four levels and each one has its own
-- identity. A project is a boulder and its route. A checkpoint is one rung of
-- that route and the unit that carries a written PRD and a status. A boulder
-- inside a checkpoint is how that checkpoint's work groups. A pebble is one
-- factory task.
--
-- Keys are natural, not synthetic. The orchestrator's commands, the light
-- factory skill's scripts, and the API's own paths all address a checkpoint as
-- (project, number), so a surrogate id would be a second name for the same
-- thing and every route would have to resolve it first. The composite primary
-- keys below are the unique indexes the shape implies:
--
--   planning_projects     project                       unique
--   planning_checkpoints  (project, number)             unique
--   planning_pebbles      (project, number, ordinal)    unique
--   planning_boulders     (project, number, boulder_id) unique
--
-- planning_pebbles carries a second unique index on (project, number, slug).
-- The slug is what the orchestrator's boulders.json named a pebble by and what
-- a human reads in a filename, so two pebbles sharing one inside a checkpoint
-- would make that name ambiguous even though their ordinals differ.
--
-- Cascading deletes rather than restricts: replacing a checkpoint's pebbles is
-- one batch that clears and rewrites both child tables, and deleting a project
-- should not leave orphan checkpoints behind for a later reader to trip over.

CREATE TABLE planning_projects (
    project TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    statement TEXT NOT NULL DEFAULT '',
    route TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- The five statuses are the closed set the cockpit styles and the transition
-- table in the store enforces. A CHECK rather than a lookup table because the
-- set is closed by design: a sixth status is a product decision that needs a
-- migration, not a row an API caller can invent. The orchestrator also wrote
-- "draft" for a PRD being written; that state is gone, because a checkpoint
-- being drafted in the agent's own context is indistinguishable from planned
-- until the draft is saved and offered for review.
--
-- frozen_at is the moment the PRD stopped changing. It is nullable because
-- most checkpoints have not reached it, and it is kept separate from
-- updated_at because answers may still be saved against a frozen checkpoint.
CREATE TABLE planning_checkpoints (
    project TEXT NOT NULL REFERENCES planning_projects(project) ON DELETE CASCADE,
    number INTEGER NOT NULL CHECK (number > 0),
    title TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'planned'
        CHECK (status IN ('planned', 'review', 'fog', 'frozen', 'built')),
    body TEXT NOT NULL DEFAULT '',
    answers TEXT NOT NULL DEFAULT '',
    frozen_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (project, number)
);

-- boulder_id is the orchestrator's own "B1", "B2", unique only inside its
-- checkpoint, so the primary key carries the checkpoint with it. ordinal is
-- the order the page draws them in, separate from the id because an id is a
-- name and a name should not have to be renumbered to reorder a list.
CREATE TABLE planning_boulders (
    project TEXT NOT NULL,
    number INTEGER NOT NULL,
    boulder_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    title TEXT NOT NULL DEFAULT '',
    statement TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (project, number, boulder_id),
    FOREIGN KEY (project, number)
        REFERENCES planning_checkpoints(project, number) ON DELETE CASCADE
);

CREATE INDEX planning_boulders_order ON planning_boulders(project, number, ordinal);

-- boulder_id is optional: a checkpoint whose pebbles were cut before anyone
-- grouped them still has to render, and the reader puts ungrouped pebbles in a
-- catch-all boulder rather than hiding them. SQLite's default composite
-- foreign key is satisfied when any child column is NULL, so the reference
-- below constrains a named boulder without forbidding an unnamed one.
--
-- work_id is the factory Work row this pebble became, set once the pebble is
-- submitted to the dark factory. It is nullable and deliberately not a foreign
-- key to sessions: a pebble outlives the Work that built it, and a Work row
-- pruned from history should not delete the plan that asked for it. The
-- roadmap's join falls back to matching by title when it is unset.
CREATE TABLE planning_pebbles (
    project TEXT NOT NULL,
    number INTEGER NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    slug TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    boulder_id TEXT,
    work_id TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (project, number, ordinal),
    FOREIGN KEY (project, number)
        REFERENCES planning_checkpoints(project, number) ON DELETE CASCADE,
    FOREIGN KEY (project, number, boulder_id)
        REFERENCES planning_boulders(project, number, boulder_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX planning_pebbles_slug ON planning_pebbles(project, number, slug);
CREATE INDEX planning_pebbles_boulder ON planning_pebbles(project, number, boulder_id);
