ALTER TABLE runs
ADD COLUMN concurrency_limit INTEGER NOT NULL DEFAULT 3
    CHECK (concurrency_limit BETWEEN 1 AND 100);

ALTER TABLE jobs
ADD COLUMN repository_identity TEXT NOT NULL DEFAULT '';

UPDATE jobs
SET repository_identity = (
    SELECT repositories.remote_identity
    FROM repositories
    WHERE repositories.id = jobs.repository_id
)
WHERE repository_identity = '';
