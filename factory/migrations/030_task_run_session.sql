-- Rename the operator model to Task, Run, and Session. The change is a pure
-- rename: no row is created, dropped, or rewritten. Execution and Attempt stay
-- as internal records and keep their table names.

-- Refuse rather than lose data when a new name is already taken, or when the
-- database never reached the Routines and Work model that migration 27 built.
-- A BEFORE INSERT trigger is used so the refusal carries a readable message.
CREATE TABLE task_rename_name_collision_guard (name TEXT PRIMARY KEY);

CREATE TRIGGER task_rename_name_collision_refuse
BEFORE INSERT ON task_rename_name_collision_guard
BEGIN
    SELECT RAISE(ABORT, 'migration 030 refused: a table named tasks, task_repositories, runs, or sessions already exists, and renaming onto it would lose data; rename or remove that table, then upgrade again');
END;

INSERT INTO task_rename_name_collision_guard(name)
SELECT name FROM sqlite_master
WHERE type = 'table'
  AND name IN ('tasks', 'task_repositories', 'runs', 'sessions');

CREATE TABLE task_rename_missing_source_guard (name TEXT PRIMARY KEY);

CREATE TRIGGER task_rename_missing_source_refuse
BEFORE INSERT ON task_rename_missing_source_guard
BEGIN
    SELECT RAISE(ABORT, 'migration 030 refused: the routines, routine_repositories, work, and work_targets tables must all exist; restore a database that completed migration 027 before upgrading');
END;

INSERT INTO task_rename_missing_source_guard(name)
SELECT required.name
FROM (
    SELECT 'routines' AS name
    UNION ALL SELECT 'routine_repositories'
    UNION ALL SELECT 'work'
    UNION ALL SELECT 'work_targets'
) required
WHERE NOT EXISTS (
    SELECT 1 FROM sqlite_master
    WHERE type = 'table' AND sqlite_master.name = required.name
);

ALTER TABLE routines RENAME TO tasks;
ALTER TABLE routine_repositories RENAME TO task_repositories;
ALTER TABLE task_repositories RENAME COLUMN routine_id TO task_id;

ALTER TABLE work RENAME TO runs;
ALTER TABLE runs RENAME COLUMN routine_id TO task_id;
ALTER TABLE runs RENAME COLUMN routine_snapshot TO task_snapshot;

ALTER TABLE work_targets RENAME TO sessions;
ALTER TABLE sessions RENAME COLUMN work_id TO run_id;

ALTER TABLE executions RENAME COLUMN work_target_id TO session_id;
ALTER TABLE workers RENAME COLUMN work_claim_protocol_version TO claim_protocol_version;

DROP INDEX routines_list_order;
DROP INDEX routines_due;
DROP INDEX routine_repositories_order;
DROP INDEX work_list_order;
DROP INDEX work_targets_work_order;
DROP INDEX work_targets_claim_order;
DROP INDEX work_targets_backend_claim;

CREATE INDEX tasks_list_order ON tasks(migration_only, archived, updated_at DESC, id DESC);
CREATE INDEX tasks_due ON tasks(schedule_enabled, schedule_retry_at, pending_due_at, next_due_at, id);
CREATE INDEX task_repositories_order ON task_repositories(task_id, position);
CREATE INDEX runs_list_order ON runs(admitted_at DESC, id DESC);
CREATE INDEX sessions_run_order ON sessions(run_id, admitted_at, id);
CREATE INDEX sessions_claim_order ON sessions(state, admitted_at, id);
CREATE INDEX sessions_backend_claim ON sessions(execution_backend, state, admitted_at, id);

-- The blocked reason is operator-visible text, so it moves to the new noun.
UPDATE sessions
SET blocked_reason = 'Waiting for an available Task concurrency slot.'
WHERE blocked_reason = 'Waiting for an available Routine concurrency slot.';

DROP TABLE task_rename_missing_source_guard;
DROP TABLE task_rename_name_collision_guard;
