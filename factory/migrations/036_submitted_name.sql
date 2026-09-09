-- Migration 36 carries the title a submitter actually wrote.
--
-- tasks.name_key is UNIQUE and admission titles repeat constantly, so
-- admission derives tasks.name by appending a deterministic hash of the
-- request key. The Drafts approval screen renders that name, which means the
-- one screen built for a human shows an internal deduplication artifact. The
-- suffix is deliberately opaque, so it cannot be stripped back off; the
-- submitted title has to be carried instead.
--
-- tasks has been a plain table since migration 030 renamed routines, and this
-- is a single ADD COLUMN, so nothing is rebuilt and no foreign key marker is
-- needed. SQLite requires an added NOT NULL column to have a non-NULL default;
-- '' is also the correct value for every Task that already exists, since only
-- admission ever has a submitted name distinct from the one it stores.
ALTER TABLE tasks ADD COLUMN submitted_name TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(submitted_name AS BLOB)) <= 1024);
