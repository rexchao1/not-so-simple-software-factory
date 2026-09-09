-- factory: foreign-keys-off

ALTER TABLE workers
ADD COLUMN accepts_managed_repositories INTEGER NOT NULL DEFAULT 0
CHECK (accepts_managed_repositories IN (0, 1));

ALTER TABLE workers
ADD COLUMN managed_repository_ids_json TEXT NOT NULL DEFAULT '[]';

ALTER TABLE repositories
ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1
CHECK (enabled IN (0, 1));

ALTER TABLE repositories
ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;

ALTER TABLE repositories
ADD COLUMN centrally_managed INTEGER NOT NULL DEFAULT 1
CHECK (centrally_managed IN (0, 1));

UPDATE repositories
SET updated_at = created_at
WHERE updated_at = 0;

ALTER TABLE worker_repositories
ADD COLUMN dynamic INTEGER NOT NULL DEFAULT 0
CHECK (dynamic IN (0, 1));

ALTER TABLE worker_repositories
ADD COLUMN worker_remote_identity TEXT NOT NULL DEFAULT '';

-- Claims sent to a pre-upgrade worker must retain the exact remote spelling
-- that worker advertised because its compatibility check is byte-for-byte.
UPDATE worker_repositories
SET worker_remote_identity = (
    SELECT repository.remote_identity
    FROM repositories repository
    WHERE repository.id = worker_repositories.repository_id
);

-- Historical worker registrations preserved GitHub owner/repository casing.
-- Collapse those aliases before making the surviving identity canonical. The
-- temporary tables also merge the unlikely case where one worker advertised
-- multiple differently cased aliases for the same repository.
CREATE TEMP TABLE repository_canonical AS
SELECT
    repository.id AS repository_id,
    lower(repository.remote_identity) AS canonical_identity,
    (
        SELECT candidate.id
        FROM repositories candidate
        WHERE lower(candidate.remote_identity) = lower(repository.remote_identity)
        ORDER BY
            CASE WHEN candidate.remote_identity = lower(candidate.remote_identity) THEN 0 ELSE 1 END,
            candidate.created_at,
            candidate.id
        LIMIT 1
    ) AS survivor_id
FROM repositories repository
WHERE lower(repository.remote_identity) LIKE 'github.com/%/%'
  AND length(repository.remote_identity) - length(replace(repository.remote_identity, '/', '')) = 2;

CREATE TABLE repository_aliases (
    alias_id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES repositories(id)
);

INSERT INTO repository_aliases(alias_id, repository_id)
SELECT repository_id, survivor_id
FROM repository_canonical
WHERE repository_id != survivor_id;

CREATE TEMP TABLE worker_repository_rank AS
SELECT
    worker_repository.worker_id,
    worker_repository.display_key,
    canonical.survivor_id,
    row_number() OVER (
        PARTITION BY worker_repository.worker_id, canonical.survivor_id
        ORDER BY
            worker_repository.advertised DESC,
            CASE WHEN worker_repository.repository_id = canonical.survivor_id THEN 0 ELSE 1 END,
            worker_repository.display_key
    ) AS rank
FROM worker_repositories worker_repository
JOIN repository_canonical canonical
  ON canonical.repository_id = worker_repository.repository_id;

UPDATE worker_repositories AS keeper
SET retained_count = (
        SELECT max(candidate.retained_count)
        FROM worker_repositories candidate
        JOIN repository_canonical canonical
          ON canonical.repository_id = candidate.repository_id
        WHERE candidate.worker_id = keeper.worker_id
          AND canonical.survivor_id = (
              SELECT rank.survivor_id
              FROM worker_repository_rank rank
              WHERE rank.worker_id = keeper.worker_id
                AND rank.display_key = keeper.display_key
          )
    ),
    advertised = (
        SELECT max(candidate.advertised)
        FROM worker_repositories candidate
        JOIN repository_canonical canonical
          ON canonical.repository_id = candidate.repository_id
        WHERE candidate.worker_id = keeper.worker_id
          AND canonical.survivor_id = (
              SELECT rank.survivor_id
              FROM worker_repository_rank rank
              WHERE rank.worker_id = keeper.worker_id
                AND rank.display_key = keeper.display_key
          )
    ),
    dynamic = (
        SELECT min(candidate.dynamic)
        FROM worker_repositories candidate
        JOIN repository_canonical canonical
          ON canonical.repository_id = candidate.repository_id
        WHERE candidate.worker_id = keeper.worker_id
          AND canonical.survivor_id = (
              SELECT rank.survivor_id
              FROM worker_repository_rank rank
              WHERE rank.worker_id = keeper.worker_id
                AND rank.display_key = keeper.display_key
          )
    ),
    updated_at = (
        SELECT max(candidate.updated_at)
        FROM worker_repositories candidate
        JOIN repository_canonical canonical
          ON canonical.repository_id = candidate.repository_id
        WHERE candidate.worker_id = keeper.worker_id
          AND canonical.survivor_id = (
              SELECT rank.survivor_id
              FROM worker_repository_rank rank
              WHERE rank.worker_id = keeper.worker_id
                AND rank.display_key = keeper.display_key
          )
    )
WHERE EXISTS (
    SELECT 1
    FROM worker_repository_rank rank
    WHERE rank.worker_id = keeper.worker_id
      AND rank.display_key = keeper.display_key
      AND rank.rank = 1
);

DELETE FROM worker_repositories
WHERE EXISTS (
    SELECT 1
    FROM worker_repository_rank rank
    WHERE rank.worker_id = worker_repositories.worker_id
      AND rank.display_key = worker_repositories.display_key
      AND rank.rank > 1
);

UPDATE worker_repositories
SET repository_id = (
    SELECT rank.survivor_id
    FROM worker_repository_rank rank
    WHERE rank.worker_id = worker_repositories.worker_id
      AND rank.display_key = worker_repositories.display_key
      AND rank.rank = 1
)
WHERE EXISTS (
    SELECT 1
    FROM worker_repository_rank rank
    WHERE rank.worker_id = worker_repositories.worker_id
      AND rank.display_key = worker_repositories.display_key
      AND rank.rank = 1
);

UPDATE tasks
SET repository_id = (
    SELECT canonical.survivor_id
    FROM repository_canonical canonical
    WHERE canonical.repository_id = tasks.repository_id
)
WHERE repository_id IN (
    SELECT repository_id FROM repository_canonical
);

DELETE FROM repositories
WHERE id IN (
    SELECT repository_id
    FROM repository_canonical
    WHERE repository_id != survivor_id
);

UPDATE repositories
SET remote_identity = lower(remote_identity)
WHERE id IN (
    SELECT survivor_id FROM repository_canonical
);

DROP TABLE worker_repository_rank;
DROP TABLE repository_canonical;
