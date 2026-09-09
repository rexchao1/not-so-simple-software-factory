-- Migration 34 keeps both the latest answer on Work and every historical
-- answer as trusted operator context. Checkpoint publication is stored
-- explicitly so a Worker can distinguish an unchanged base checkpoint from a
-- commit that must remain reachable through the immutable publish ref.
ALTER TABLE sessions ADD COLUMN answer TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(answer AS BLOB)) <= 8192);
ALTER TABLE sessions ADD COLUMN checkpoint_published INTEGER NOT NULL DEFAULT 0
    CHECK (checkpoint_published IN (0, 1));

ALTER TABLE work_updates ADD COLUMN checkpoint_published INTEGER NOT NULL DEFAULT 0
    CHECK (checkpoint_published IN (0, 1));

CREATE TABLE work_answers (
    id TEXT PRIMARY KEY,
    work_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    question_update_id TEXT NOT NULL REFERENCES work_updates(id),
    request_id TEXT NOT NULL CHECK (length(CAST(request_id AS BLOB)) BETWEEN 1 AND 200),
    message TEXT NOT NULL CHECK (length(CAST(message AS BLOB)) BETWEEN 1 AND 8192),
    accepted_at INTEGER NOT NULL,
    UNIQUE (work_id, request_id),
    UNIQUE (question_update_id)
);

CREATE INDEX work_answers_work_order ON work_answers(work_id, accepted_at, id);
