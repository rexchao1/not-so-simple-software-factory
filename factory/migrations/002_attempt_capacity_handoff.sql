-- Schema 1 had no durable local record for an active attempt. Refuse to guess
-- whether its worktree would be retained or disposed after interruption.
CREATE TABLE migration_002_active_attempt_guard (
    active_count INTEGER
        CONSTRAINT drain_active_v2_attempts_before_upgrade
        CHECK (active_count = 0)
);

INSERT INTO migration_002_active_attempt_guard(active_count)
SELECT
    (
        SELECT COUNT(*)
        FROM attempts
        WHERE state IN ('preparing', 'running')
    )
    +
    (
        SELECT COUNT(*)
        FROM workers
        WHERE active_count <> 0
    );

DROP TABLE migration_002_active_attempt_guard;

ALTER TABLE attempts
ADD COLUMN capacity_acknowledged INTEGER NOT NULL DEFAULT 0
CHECK (capacity_acknowledged IN (0, 1));

-- Schema 1 had no durable per-attempt local disposition. Preserve its existing
-- retained-worktree snapshot instead of treating historical terminal rows as
-- new reservations that no upgraded worker can acknowledge. Schema 1 workers
-- left retained_count at zero, so derive it from complete unique summaries.
UPDATE worker_repositories AS wr
SET retained_count = MAX(
    retained_count,
    COALESCE((
        SELECT COUNT(DISTINCT CASE
            WHEN retained.type = 'object'
            THEN json_extract(retained.value, '$.attempt_id')
        END)
        FROM workers AS w,
             json_each(
                 CASE
                     WHEN json_valid(w.retained_worktrees_json)
                     THEN CASE
                         WHEN json_type(w.retained_worktrees_json) = 'array'
                         THEN w.retained_worktrees_json
                         ELSE '[]'
                     END
                     ELSE '[]'
                 END
             ) AS retained
        WHERE w.id = wr.worker_id
          AND retained.type = 'object'
          AND TRIM(COALESCE(json_extract(retained.value, '$.attempt_id'), '')) <> ''
          AND json_extract(retained.value, '$.repository_id') = wr.repository_id
    ), 0)
);

UPDATE attempts AS historical
SET capacity_acknowledged = 1
WHERE state IN ('succeeded', 'failed', 'cancelled', 'lost')
  AND EXISTS (
      SELECT 1
      FROM workers AS w
      WHERE w.id = historical.worker_id
        AND json_valid(w.retained_worktrees_json)
        AND json_type(
            CASE
                WHEN json_valid(w.retained_worktrees_json)
                THEN w.retained_worktrees_json
                ELSE 'null'
            END
        ) = 'array'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM workers AS w,
           json_each(
               CASE
                   WHEN json_valid(w.retained_worktrees_json)
                   THEN CASE
                       WHEN json_type(w.retained_worktrees_json) = 'array'
                       THEN w.retained_worktrees_json
                       ELSE '[]'
                   END
                   ELSE '[]'
               END
           ) AS retained
      WHERE w.id = historical.worker_id
        AND (
            retained.type <> 'object'
            OR TRIM(COALESCE(
                CASE WHEN retained.type = 'object'
                     THEN json_extract(retained.value, '$.attempt_id')
                END,
                ''
            )) = ''
            OR TRIM(COALESCE(
                CASE WHEN retained.type = 'object'
                     THEN json_extract(retained.value, '$.repository_id')
                END,
                ''
            )) = ''
            OR NOT EXISTS (
                SELECT 1
                FROM worker_repositories AS attributed
                WHERE attributed.worker_id = historical.worker_id
                  AND attributed.advertised = 1
                  AND attributed.repository_id = CASE
                      WHEN retained.type = 'object'
                      THEN json_extract(retained.value, '$.repository_id')
                  END
            )
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM (
          SELECT
              CASE WHEN retained.type = 'object'
                   THEN json_extract(retained.value, '$.attempt_id')
              END AS attempt_id
          FROM workers AS w,
               json_each(
                   CASE
                       WHEN json_valid(w.retained_worktrees_json)
                       THEN CASE
                           WHEN json_type(w.retained_worktrees_json) = 'array'
                           THEN w.retained_worktrees_json
                           ELSE '[]'
                       END
                       ELSE '[]'
                   END
               ) AS retained
          WHERE w.id = historical.worker_id
          GROUP BY attempt_id
          HAVING COUNT(DISTINCT CASE
              WHEN retained.type = 'object'
              THEN json_extract(retained.value, '$.repository_id')
          END) > 1
      ) AS conflicting_summary
  );

CREATE INDEX attempts_unacknowledged_capacity
ON attempts(worker_id, capacity_acknowledged, state);
