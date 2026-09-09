-- 045: the Work list.
--
-- The Work board becomes one card per Work row rather than one per Run. A Run
-- can span several repositories, so a Run card in a repository tab describes
-- work in repositories the operator did not ask about. Serving that board
-- needs two things the schema does not have.
--
-- 1. sessions.updated_at. A card shows when its Work last changed, and the
--    three timestamps that exist cannot answer it: admitted_at never moves,
--    started_at moves once, and terminal_at is not monotonic because
--    updateRunLifecycle, ApproveWork, AnswerWork and RetrySession all
--    recompute it and a retry sets it back to NULL. Deriving the value
--    instead would mean a COALESCE over those three plus a correlated MAX
--    over session_stages on every row of every page.
--
--    Existing rows are backfilled from the newest timestamp each one already
--    has, so a Work admitted before this migration sorts by something real
--    rather than by zero. This column is for display and is deliberately NOT
--    a cursor key: only admitted_at is stable enough to page on.
--
-- 2. An index for the list query. sessions has indexes on (run_id, ...),
--    (state, admitted_at, id) and (repository_id, source_kind, source_key,
--    state), so a board filtered by repository and ordered by admitted_at can
--    use none of them end to end: the repository index carries no timestamp
--    and would sort the whole repository, and the claim index carries no
--    repository and would read then discard other repositories' rows.
--
--    Two indexes rather than one, because the board has two shapes. The
--    unfiltered board pages every repository at once and needs the plain
--    (admitted_at, id) ordering, the analogue of runs_list_order. A
--    repository tab needs repository_id ahead of that ordering. State is left
--    out of both: the board filters to several states at a time, and SQLite
--    cannot use a middle index column for IN and still return the following
--    columns in order.

ALTER TABLE sessions ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;

UPDATE sessions SET updated_at = MAX(
    admitted_at,
    COALESCE(started_at, 0),
    COALESCE(terminal_at, 0),
    COALESCE(approved_at, 0),
    COALESCE(delivery_verified_at, 0)
);

CREATE INDEX sessions_list_order ON sessions(admitted_at, id);
CREATE INDEX sessions_repository_list ON sessions(repository_id, admitted_at, id);

-- Twenty-five statements across nine files update a session, and every future
-- one would have to remember this column. A trigger cannot be forgotten.
--
-- The WHEN guard does two jobs. It leaves an explicitly written updated_at
-- alone, so a caller that wants the Store's own clock can still set it. And it
-- stops the trigger's own UPDATE from re-firing, since that statement always
-- changes the column, without depending on recursive_triggers being off.
--
-- julianday rather than unixepoch('subsec'): the same arithmetic works on
-- every SQLite version, and this is a display timestamp, so wall-clock time is
-- the honest source for a write that named no time of its own.
CREATE TRIGGER sessions_touch_updated_at
AFTER UPDATE ON sessions
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE sessions
    SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000.0 AS INTEGER)
    WHERE id = NEW.id;
END;

-- A row's default of 0 would sort every freshly admitted Work item to the
-- bottom of the board and render as 1970. Insert statements do not name the
-- column either, so the same argument as above applies: seed it from the
-- admission time the insert did supply.
CREATE TRIGGER sessions_seed_updated_at
AFTER INSERT ON sessions
FOR EACH ROW
WHEN NEW.updated_at = 0
BEGIN
    UPDATE sessions SET updated_at = NEW.admitted_at WHERE id = NEW.id;
END;
