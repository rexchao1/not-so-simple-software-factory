-- factory: foreign-keys-off
--
-- 039: auto-merge.
--
-- Three things, each with its own reason.
--
-- 1. session_stages.review_verdict. INV-8 gates merge on a review verdict of
--    Approve, and no such value exists anywhere in the system today. It lives
--    on the stage rather than on the outcome so that the verdict is recorded
--    by a process that provably did not write the code: session_stages.position
--    distinguishes the reviewer from the implementer. The empty string is the
--    default and means "no verdict recorded", on which INV-8 fails closed.
--
-- 2. sessions.delivery_verified_at and delivery_verification_source. INV-3
--    requires the SERVER to verify a ready delivery. Recording how it was
--    verified makes a ready accepted before this migration distinguishable
--    from one the server checked itself. One clause (local HEAD on an
--    off-host worker) stays unverified; see verifyReadyDelivery.
--
-- 3. work_updates 'merged'. A merge is recorded in the outcome ledger as the
--    first row ever written with actor = 'system'. It needs its own status,
--    and that status must be exempted from work_updates_attempt_outcome, which
--    is UNIQUE(attempt_id) WHERE attempt_id IS NOT NULL AND status != 'running'
--    and would otherwise collide with the attempt's existing ready row.
--
-- The work_updates rebuild below is a faithful copy of the table as it stands
-- at 038, and it is faithful on purpose. SQLite cannot alter a CHECK in place,
-- so the only way to widen the status CHECK is DROP TABLE, and DROP TABLE also
-- takes every index and every trigger with it. The full prior state is
-- 031_work_lifecycle.sql:180-227 plus the checkpoint_published column added by
-- 034_resume_recovery.sql:10. Exactly three things differ from that state:
-- 'merged' joins the status CHECK, 'merged' is exempted from the outcome
-- index, and a merged row must carry the pull request URL it merged, which is
-- the same shape as the ready row's existing CHECK.
-- TestMigration039PreservesTheWorkUpdatesTable is the guard on the rest.

ALTER TABLE session_stages ADD COLUMN review_verdict TEXT NOT NULL DEFAULT ''
  CHECK (review_verdict IN ('', 'approve', 'request-changes', 'blocked'));

ALTER TABLE sessions ADD COLUMN delivery_verified_at INTEGER;
ALTER TABLE sessions ADD COLUMN delivery_verification_source TEXT NOT NULL DEFAULT ''
  CHECK (delivery_verification_source IN ('', 'server-github'));

CREATE TABLE work_updates_auto_merge (
    id TEXT PRIMARY KEY,
    work_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    attempt_id TEXT REFERENCES attempts(id),
    request_id TEXT NOT NULL CHECK (length(CAST(request_id AS BLOB)) BETWEEN 1 AND 200),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    status TEXT NOT NULL CHECK (status IN ('running', 'ready', 'needs-input', 'failed', 'no-change', 'merged')),
    message TEXT NOT NULL CHECK (
        length(CAST(message AS BLOB)) BETWEEN 1 AND
        CASE WHEN status = 'running' THEN 2048 ELSE 8192 END
    ),
    pull_request_url TEXT NOT NULL DEFAULT '' CHECK (length(CAST(pull_request_url AS BLOB)) <= 2048),
    pull_request_head_branch TEXT NOT NULL DEFAULT '' CHECK (length(CAST(pull_request_head_branch AS BLOB)) <= 255),
    pull_request_head_sha TEXT NOT NULL DEFAULT '' CHECK (length(pull_request_head_sha) <= 64),
    checkpoint_sha TEXT NOT NULL DEFAULT '' CHECK (length(checkpoint_sha) <= 64),
    actor TEXT NOT NULL CHECK (actor IN ('agent', 'operator', 'system')),
    accepted_at INTEGER NOT NULL,
    checkpoint_published INTEGER NOT NULL DEFAULT 0 CHECK (checkpoint_published IN (0, 1)),
    UNIQUE (work_id, sequence),
    CHECK (status != 'ready' OR pull_request_url != ''),
    CHECK (status != 'needs-input' OR checkpoint_sha != ''),
    CHECK (status != 'merged' OR pull_request_url != '')
);

INSERT INTO work_updates_auto_merge (
    id, work_id, attempt_id, request_id, sequence, status, message,
    pull_request_url, pull_request_head_branch, pull_request_head_sha,
    checkpoint_sha, actor, accepted_at, checkpoint_published
)
SELECT
    id, work_id, attempt_id, request_id, sequence, status, message,
    pull_request_url, pull_request_head_branch, pull_request_head_sha,
    checkpoint_sha, actor, accepted_at, checkpoint_published
FROM work_updates;

DROP TABLE work_updates;
ALTER TABLE work_updates_auto_merge RENAME TO work_updates;

CREATE UNIQUE INDEX work_updates_attempt_request
    ON work_updates(attempt_id, request_id) WHERE attempt_id IS NOT NULL;
CREATE UNIQUE INDEX work_updates_operator_request
    ON work_updates(work_id, request_id) WHERE attempt_id IS NULL;
-- The one substantive index change: a merge row rides alongside the attempt's
-- existing ready row, so it is exempt from the one-outcome-per-attempt rule.
CREATE UNIQUE INDEX work_updates_attempt_outcome
    ON work_updates(attempt_id) WHERE attempt_id IS NOT NULL AND status != 'running' AND status != 'merged';
CREATE INDEX work_updates_work_order ON work_updates(work_id, sequence);

CREATE TRIGGER work_updates_attempt_limit
BEFORE INSERT ON work_updates
WHEN NEW.attempt_id IS NOT NULL AND (
    SELECT COUNT(*) FROM work_updates WHERE attempt_id = NEW.attempt_id
) >= 200
BEGIN
    SELECT RAISE(ABORT, 'work update limit reached');
END;

CREATE TRIGGER work_updates_progress_limit
BEFORE INSERT ON work_updates
WHEN NEW.attempt_id IS NOT NULL AND NEW.status = 'running' AND (
    SELECT COUNT(*) FROM work_updates
    WHERE attempt_id = NEW.attempt_id AND status = 'running'
) >= 199
BEGIN
    SELECT RAISE(ABORT, 'work progress update limit reached');
END;
