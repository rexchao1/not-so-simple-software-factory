-- 040: answer actor.
--
-- Every answer to a needs-input question records who gave it, the way
-- sessions.approved_by records who approved. work_answers.actor is the label
-- on the answer itself and sessions.answered_by is its projection on the Work,
-- cleared together with sessions.answer when the next question arrives. Both
-- are plain ADD COLUMN: neither table is rebuilt and no CHECK beyond the byte
-- bounds applies, because the actor is a free-form label.
--
-- Every answer recorded before this migration was given by the operator, so
-- the answer column defaults to that label and the Work projection is
-- backfilled wherever an answer is present.

ALTER TABLE work_answers ADD COLUMN actor TEXT NOT NULL DEFAULT 'operator'
  CHECK (length(CAST(actor AS BLOB)) BETWEEN 1 AND 255);

ALTER TABLE sessions ADD COLUMN answered_by TEXT NOT NULL DEFAULT ''
  CHECK (length(CAST(answered_by AS BLOB)) <= 255);

UPDATE sessions SET answered_by = 'operator' WHERE answer != '';
