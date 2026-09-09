-- 047: read-only stages, and the planning triple on a Work row.
--
-- Two additions, both in service of the critique pass the light factory runs.
--
-- 1. read_only on a stage. The critic reads a repository and writes findings;
--    it must not be able to change the tree. Decision 2 of the light and dark
--    PRD says that guarantee is the worker's, not a prompt's, so the flag has
--    to survive the freeze: it is copied from pipeline_stages onto
--    session_stages at admission the same way kind, model and effort are, and
--    the worker turns it into a runtime argument. A stage frozen before this
--    migration is not read only, which is what the default expresses.
--
--    The columns carry only their 0-or-1 domain. The other half of the rule,
--    that read_only belongs to an agent stage and never to a code or delivery
--    stage, is checked in normalizePipeline and again when stages are frozen.
--    Expressing it in the schema means rebuilding both tables to restate their
--    existing kind CHECK, and these are the two tables every Run and every
--    claim reads. The rule is worth less than that risk, and it cannot be
--    reached without passing a validator that already refuses it.
--
-- 2. The planning triple on sessions. A critique Work item is admitted against
--    one checkpoint and one round of review on it, and the roadmap has to be
--    able to ask "what passes has this checkpoint had" without guessing from
--    a task name. The triple is what step C1 said it would join on.
--
--    All three columns are nullable together: almost no Work is a planning
--    pass, and a NULL project is how a row says so. They are not foreign keys
--    to planning_checkpoints, for the same reason planning_pebbles.work_id is
--    not a foreign key to sessions: the two halves outlive each other, and a
--    checkpoint deleted after its critique ran should not take the Work row
--    and its recorded cost with it.
--
--    One index over all three columns, because every read is "the passes on
--    this checkpoint" and orders them by round. A prefix of it also answers
--    "the passes on this project", so a second index would be a copy.

ALTER TABLE pipeline_stages ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0
    CHECK (read_only IN (0, 1));

ALTER TABLE session_stages ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0
    CHECK (read_only IN (0, 1));

ALTER TABLE sessions ADD COLUMN planning_project TEXT;
ALTER TABLE sessions ADD COLUMN planning_number INTEGER;
ALTER TABLE sessions ADD COLUMN planning_round INTEGER;

CREATE INDEX sessions_planning
ON sessions(planning_project, planning_number, planning_round)
WHERE planning_project IS NOT NULL;
