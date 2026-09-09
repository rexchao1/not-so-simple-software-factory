-- factory: foreign-keys-off

-- The Routines model is a pre-launch replacement, not a compatibility layer.
-- Refuse states that cannot be converted without changing live behavior.
CREATE TABLE routines_migration_guard (
    reason TEXT PRIMARY KEY,
    invalid_count INTEGER NOT NULL CHECK (invalid_count = 0)
);

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'active executions', COUNT(*)
FROM executions
WHERE state IN ('queued', 'preparing', 'running');

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'enabled provider automations', COUNT(*)
FROM automations
WHERE enabled = 1 AND trigger_type != 'schedule';

-- Definitions did not own repository scope, so each repository-backed legacy
-- schedule is necessarily a split Routine under the final one-schedule model.
INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'operator-authored Routine count over 500',
       MAX((SELECT COUNT(*) FROM definitions) +
           (SELECT COUNT(*) FROM automation_schedule_triggers) +
           (SELECT COUNT(*) FROM workflows) - 500, 0);

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'legacy schedule repository scope over 100', COUNT(*)
FROM (
    SELECT automation_id
    FROM automation_schedule_repositories
    GROUP BY automation_id
    HAVING COUNT(*) > 100
);

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'pending schedule repository scope over 100', COUNT(*)
FROM automation_schedule_occurrences scheduled
JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
WHERE scheduled.kind = 'scheduled'
  AND scheduled.run_id IS NULL
  AND occurrence.state IN ('pending', 'dispatching', 'failed')
  AND json_array_length(scheduled.repository_ids_json) > 100;

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'unfinished workflow-backed schedule triggers', COUNT(*)
FROM automations automation
JOIN automation_schedule_triggers trigger ON trigger.automation_id = automation.id
WHERE trigger.definition_id IS NULL
  AND automation.workflow_id IS NOT NULL;

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'deleted-Task occurrence tombstones', COUNT(*)
FROM automation_occurrences
WHERE state = 'task_deleted';

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'taskless provider occurrences', COUNT(*)
FROM automation_occurrences occurrence
JOIN automations automation ON automation.id = occurrence.automation_id
LEFT JOIN automation_github_webhook_occurrences webhook
  ON webhook.occurrence_id = occurrence.id
WHERE automation.trigger_type != 'schedule'
  AND occurrence.task_id IS NULL
  AND webhook.run_id IS NULL;

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'Definition prompts over 64 KiB after folding inputs', COUNT(*)
FROM definitions
WHERE length(CAST(
    prompt || CASE WHEN json(inputs) = '{}' THEN ''
    ELSE char(10) || char(10) || 'Trusted Factory Run parameters:' ||
         char(10) || char(10) || json(inputs) END
    AS BLOB
)) > 65536;

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'schedule overrides missing from current Definition', COUNT(*)
FROM automation_schedule_triggers trigger
JOIN definitions definition ON definition.id = trigger.definition_id
WHERE EXISTS (
    SELECT 1
    FROM json_each(trigger.parameters_json) override
    WHERE NOT EXISTS (
        SELECT 1 FROM json_each(definition.inputs) declared
        WHERE declared.key = override.key
    )
);

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'schedule prompts over 64 KiB after folding inputs', COUNT(*)
FROM automation_schedule_triggers trigger
JOIN definitions definition ON definition.id = trigger.definition_id
WHERE length(CAST(
    definition.prompt || CASE
        WHEN json_patch(definition.inputs, trigger.parameters_json) = '{}' THEN ''
        ELSE char(10) || char(10) || 'Trusted Factory Run parameters:' ||
             char(10) || char(10) || json(json_patch(definition.inputs, trigger.parameters_json))
    END || char(10) || char(10) || 'Schedule instruction:' || char(10) || char(10) ||
    'Execute this Routine for the scheduled occurrence. There is no provider item to revalidate.' ||
    char(10) || char(10) || 'Trusted schedule occurrence:' || char(10) || char(10) ||
    json_object(
        'type', 'schedule',
        'scheduled_at', '9999-12-31T23:59:59.999999999Z',
        'cron', trigger.cron,
        'timezone', trigger.timezone
    ) AS BLOB
)) > 65536;

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'pending schedule prompts over 64 KiB after folding inputs', COUNT(*)
FROM automation_schedule_occurrences scheduled
JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
WHERE scheduled.kind = 'scheduled' AND scheduled.run_id IS NULL
  AND occurrence.state IN ('pending', 'dispatching', 'failed')
  AND length(CAST(
      json_extract(scheduled.definition_snapshot, '$.prompt') || CASE
          WHEN json_patch(
              COALESCE(json_extract(scheduled.definition_snapshot, '$.inputs'), '{}'),
              scheduled.parameters_json
          ) = '{}' THEN ''
          ELSE char(10) || char(10) || 'Trusted Factory Run parameters:' ||
               char(10) || char(10) || json(json_patch(
                   COALESCE(json_extract(scheduled.definition_snapshot, '$.inputs'), '{}'),
                   scheduled.parameters_json
               ))
      END || char(10) || char(10) || 'Schedule instruction:' || char(10) || char(10) ||
      'Execute this Routine for the scheduled occurrence. There is no provider item to revalidate.' ||
      char(10) || char(10) || 'Trusted schedule occurrence:' || char(10) || char(10) ||
      json_object(
          'type', 'schedule',
          'scheduled_at', '9999-12-31T23:59:59.999999999Z',
          'cron', scheduled.cron,
          'timezone', scheduled.timezone
      ) AS BLOB
  )) > 65536;

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'pending schedule run-now admissions', COUNT(*)
FROM automation_occurrences occurrence
JOIN automation_schedule_occurrences scheduled
  ON scheduled.occurrence_id = occurrence.id
WHERE scheduled.kind = 'run_now'
  AND occurrence.state IN ('pending', 'dispatching', 'failed')
  AND scheduled.run_id IS NULL;

INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'incomplete pending scheduled admissions', COUNT(*)
FROM automation_occurrences occurrence
JOIN automation_schedule_occurrences scheduled
  ON scheduled.occurrence_id = occurrence.id
WHERE scheduled.kind = 'scheduled'
  AND occurrence.state IN ('pending', 'dispatching', 'failed')
  AND scheduled.run_id IS NULL
  AND (
      scheduled.scheduled_at IS NULL
      OR scheduled.definition_snapshot IS NULL
      OR scheduled.repository_ids_json IS NULL
  );

CREATE TABLE routines (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL UNIQUE,
    prompt TEXT NOT NULL CHECK (length(prompt) <= 65536),
    runtime TEXT NOT NULL CHECK (runtime IN ('pi', 'codex', 'claude-code')),
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 28800),
    concurrency_limit INTEGER NOT NULL DEFAULT 10 CHECK (concurrency_limit BETWEEN 1 AND 100),
    generation INTEGER NOT NULL CHECK (generation >= 1),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    migration_only INTEGER NOT NULL DEFAULT 0 CHECK (migration_only IN (0, 1)),
    read_only INTEGER NOT NULL DEFAULT 0 CHECK (read_only IN (0, 1)),
    schedule_enabled INTEGER NOT NULL DEFAULT 0 CHECK (schedule_enabled IN (0, 1)),
    cron TEXT,
    timezone TEXT,
    next_due_at INTEGER,
    pending_due_at INTEGER,
    schedule_retry_at INTEGER,
    schedule_retry_count INTEGER NOT NULL DEFAULT 0 CHECK (schedule_retry_count >= 0),
    pending_snapshot_json TEXT
        CHECK (pending_snapshot_json IS NULL OR (
            json_valid(pending_snapshot_json) AND json_type(pending_snapshot_json) = 'object'
        )),
    last_discarded_due_at INTEGER,
    schedule_health_status TEXT NOT NULL DEFAULT 'disabled'
        CHECK (schedule_health_status IN ('disabled', 'healthy', 'blocked', 'error')),
    schedule_health_code TEXT NOT NULL DEFAULT '',
    schedule_health_message TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (
        (cron IS NULL AND timezone IS NULL AND schedule_enabled = 0)
        OR (cron IS NOT NULL AND timezone IS NOT NULL)
    ),
    CHECK (migration_only = 0 OR (archived = 1 AND schedule_enabled = 0)),
    CHECK (read_only = 0 OR (archived = 1 AND schedule_enabled = 0))
);

CREATE TABLE routine_repositories (
    routine_id TEXT NOT NULL REFERENCES routines(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    PRIMARY KEY (routine_id, repository_id),
    UNIQUE (routine_id, position)
);

CREATE TABLE work (
    id TEXT PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    request_digest BLOB NOT NULL,
    routine_id TEXT NOT NULL REFERENCES routines(id),
    routine_snapshot TEXT NOT NULL
        CHECK (json_valid(routine_snapshot) AND json_type(routine_snapshot) = 'object'),
    source TEXT NOT NULL CHECK (source IN ('manual', 'schedule', 'provider_history')),
    scheduled_at INTEGER,
    provider_snapshot TEXT
        CHECK (provider_snapshot IS NULL OR (
            json_valid(provider_snapshot) AND json_type(provider_snapshot) = 'object'
        )),
    admitted_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    terminal_at INTEGER
);

CREATE TABLE work_targets (
    id TEXT PRIMARY KEY,
    work_id TEXT NOT NULL REFERENCES work(id) ON DELETE CASCADE,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    repository_identity TEXT NOT NULL,
    resolved_prompt TEXT NOT NULL,
    required_runtime TEXT NOT NULL CHECK (required_runtime IN ('pi', 'codex', 'claude-code')),
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 28800),
    state TEXT NOT NULL
        CHECK (state IN ('blocked', 'queued', 'preparing', 'running', 'succeeded', 'failed', 'cancelled')),
    blocked_reason TEXT,
    assigned_worker_id TEXT REFERENCES workers(id),
    cancellation_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancellation_requested IN (0, 1)),
    retry_may_repeat_effects INTEGER NOT NULL DEFAULT 0 CHECK (retry_may_repeat_effects IN (0, 1)),
    admitted_at INTEGER NOT NULL,
    started_at INTEGER,
    terminal_at INTEGER,
    result TEXT,
    failure_reason TEXT,
    UNIQUE (work_id, repository_id)
);

CREATE INDEX routines_list_order ON routines(migration_only, archived, updated_at DESC, id DESC);
CREATE INDEX routines_due ON routines(schedule_enabled, schedule_retry_at, pending_due_at, next_due_at, id);
CREATE INDEX routine_repositories_order ON routine_repositories(routine_id, position);
CREATE INDEX work_list_order ON work(admitted_at DESC, id DESC);
CREATE INDEX work_targets_work_order ON work_targets(work_id, admitted_at, id);
CREATE INDEX work_targets_claim_order ON work_targets(state, admitted_at, id);

-- Definitions become draft Routines. Input defaults are folded into the prompt
-- because Routines deliberately have no parameter schema.
WITH named_definitions AS (
    SELECT definition.*, ROW_NUMBER() OVER (ORDER BY definition.id) AS migration_position
    FROM definitions definition
)
INSERT INTO routines(
    id, name, name_key, prompt, runtime, timeout_seconds,
    concurrency_limit, generation, archived, migration_only,
    schedule_enabled, schedule_health_status, schedule_health_message,
    created_at, updated_at
)
SELECT
    definition.id,
    definition.name || ' · definition ' || definition.migration_position,
    lower(trim(definition.name || ' · definition ' || definition.migration_position)),
    definition.prompt || CASE
        WHEN json(definition.inputs) = '{}' THEN ''
        ELSE char(10) || char(10) || 'Trusted Factory Run parameters:' || char(10) || char(10) || json(definition.inputs)
    END,
    definition.runtime,
    definition.timeout_seconds,
    10,
    definition.generation,
    definition.archived,
    0,
    0,
    'disabled',
    '',
    definition.created_at,
    definition.updated_at
FROM named_definitions definition;

-- Every legacy schedule receives a Routine. This avoids collapsing multiple
-- cadences or execution settings into the one-schedule final model.
WITH named_schedules AS (
    SELECT automation.*,
           (SELECT COUNT(*) FROM definitions) +
           ROW_NUMBER() OVER (ORDER BY automation.id) AS migration_position
    FROM automations automation
    JOIN automation_schedule_triggers trigger ON trigger.automation_id = automation.id
)
INSERT INTO routines(
    id, name, name_key, prompt, runtime, timeout_seconds,
    concurrency_limit, generation, archived, migration_only,
    schedule_enabled, cron, timezone, next_due_at,
    schedule_health_status, schedule_health_code, schedule_health_message,
    created_at, updated_at
)
SELECT
    automation.id,
    automation.title || ' · schedule ' || automation.migration_position,
    lower(trim(automation.title || ' · schedule ' || automation.migration_position)),
    definition.prompt || CASE
        WHEN json_patch(definition.inputs, trigger.parameters_json) = '{}' THEN ''
        ELSE char(10) || char(10) || 'Trusted Factory Run parameters:' ||
             char(10) || char(10) || json(json_patch(definition.inputs, trigger.parameters_json))
    END,
    definition.runtime,
    definition.timeout_seconds,
    trigger.concurrency_limit,
    automation.version,
    definition.archived,
    0,
    CASE WHEN definition.archived = 1 THEN 0 ELSE automation.enabled END,
    trigger.cron,
    trigger.timezone,
    CASE WHEN definition.archived = 1 THEN NULL ELSE trigger.next_due_at END,
    CASE WHEN definition.archived = 0 AND automation.enabled = 1 THEN 'healthy' ELSE 'disabled' END,
    CASE WHEN definition.archived = 1 THEN 'source_archived' ELSE automation.health_code END,
    CASE WHEN definition.archived = 1
        THEN 'Archived because its source prompt was archived before migration.'
        ELSE automation.health_message END,
    automation.created_at,
    automation.updated_at
FROM named_schedules automation
JOIN automation_schedule_triggers trigger ON trigger.automation_id = automation.id
JOIN definitions definition ON definition.id = trigger.definition_id;

INSERT INTO routine_repositories(routine_id, position, repository_id)
SELECT automation_id, position, repository_id
FROM automation_schedule_repositories;

-- Every saved Runbook revision remains inspectable as a Routine even when it
-- never produced Task history. The current revision is a draft; older
-- revisions are archived.
WITH named_workflows AS (
    SELECT workflow.id AS workflow_id, workflow.enabled,
           workflow.current_revision_id, workflow.updated_at,
           revision.id AS revision_id, revision.revision_number,
           revision.title, revision.summary, revision.instructions,
           revision.created_at,
           (SELECT COUNT(*) FROM definitions) +
           (SELECT COUNT(*) FROM automation_schedule_triggers) +
           DENSE_RANK() OVER (ORDER BY workflow.id) AS migration_position
    FROM workflows workflow
    JOIN workflow_revisions revision ON revision.workflow_id = workflow.id
)
INSERT INTO routines(
    id, name, name_key, prompt, runtime, timeout_seconds,
    concurrency_limit, generation, archived, migration_only, read_only, schedule_enabled,
    schedule_health_status, created_at, updated_at
)
SELECT
    workflow.revision_id,
    workflow.title || ' · workflow ' || workflow.migration_position ||
        ' · revision ' || workflow.revision_number,
    lower(trim(workflow.title || ' · workflow ' || workflow.migration_position ||
        ' · revision ' || workflow.revision_number)),
    'Runbook instructions:' || char(10) || char(10) || workflow.instructions ||
    CASE WHEN workflow.summary = '' THEN '' ELSE
        char(10) || char(10) || 'Runbook summary:' || char(10) || char(10) || workflow.summary
    END,
    'codex',
    7200,
    10,
    workflow.revision_number,
    CASE WHEN workflow.enabled = 1 AND workflow.revision_id = workflow.current_revision_id
        THEN 0 ELSE 1 END,
    0,
    CASE WHEN workflow.revision_id = workflow.current_revision_id THEN 0 ELSE 1 END,
    0,
    'disabled',
    workflow.created_at,
    CASE WHEN workflow.revision_id = workflow.current_revision_id
        THEN workflow.updated_at ELSE workflow.created_at END
FROM named_workflows workflow;

-- At most one pending scheduled occurrence per Routine can exist in the final
-- model. Refuse more rather than losing an occurrence.
INSERT INTO routines_migration_guard(reason, invalid_count)
SELECT 'multiple pending occurrences for schedule ' || scheduled.automation_id, COUNT(*) - 1
FROM automation_occurrences occurrence
JOIN automation_schedule_occurrences scheduled
  ON scheduled.occurrence_id = occurrence.id
WHERE scheduled.kind = 'scheduled'
  AND occurrence.state IN ('pending', 'dispatching', 'failed')
  AND scheduled.run_id IS NULL
GROUP BY scheduled.automation_id
HAVING COUNT(*) > 1;

UPDATE routines
SET
    pending_due_at = (
        SELECT scheduled.scheduled_at
        FROM automation_schedule_occurrences scheduled
        JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
        WHERE scheduled.automation_id = routines.id
          AND scheduled.kind = 'scheduled'
          AND occurrence.state IN ('pending', 'dispatching', 'failed')
          AND scheduled.run_id IS NULL
        LIMIT 1
    ),
    schedule_retry_at = (
        SELECT CASE
            WHEN occurrence.state = 'failed' THEN NULL
            ELSE COALESCE(occurrence.retry_at, occurrence.updated_at)
        END
        FROM automation_schedule_occurrences scheduled
        JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
        WHERE scheduled.automation_id = routines.id
          AND scheduled.kind = 'scheduled'
          AND occurrence.state IN ('pending', 'dispatching', 'failed')
          AND scheduled.run_id IS NULL
        LIMIT 1
    ),
    pending_snapshot_json = (
        SELECT json_object(
            'name', routines.name,
            'prompt', json_extract(scheduled.definition_snapshot, '$.prompt') || CASE
                WHEN json_patch(
                    COALESCE(json_extract(scheduled.definition_snapshot, '$.inputs'), '{}'),
                    scheduled.parameters_json
                ) = '{}' THEN ''
                ELSE char(10) || char(10) || 'Trusted Factory Run parameters:' ||
                     char(10) || char(10) || json(json_patch(
                         COALESCE(json_extract(scheduled.definition_snapshot, '$.inputs'), '{}'),
                         scheduled.parameters_json
                     ))
            END,
            'runtime', json_extract(scheduled.definition_snapshot, '$.runtime'),
            'timeout_seconds', json_extract(scheduled.definition_snapshot, '$.timeout_seconds'),
            'concurrency_limit', scheduled.concurrency_limit,
            'repository_ids', json(scheduled.repository_ids_json),
            'generation', json_extract(scheduled.definition_snapshot, '$.generation'),
            'cron', scheduled.cron,
            'timezone', scheduled.timezone
        )
        FROM automation_schedule_occurrences scheduled
        JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
        WHERE scheduled.automation_id = routines.id
          AND scheduled.kind = 'scheduled'
          AND occurrence.state IN ('pending', 'dispatching', 'failed')
          AND scheduled.run_id IS NULL
        LIMIT 1
    ),
    schedule_health_status = CASE
        WHEN schedule_enabled = 0 THEN 'disabled'
        WHEN EXISTS (
            SELECT 1
            FROM automation_schedule_occurrences scheduled
            JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
            WHERE scheduled.automation_id = routines.id
              AND scheduled.kind = 'scheduled' AND scheduled.run_id IS NULL
              AND occurrence.state = 'failed'
        ) THEN 'blocked'
        ELSE schedule_health_status
    END,
    schedule_health_code = CASE
        WHEN archived = 1 THEN schedule_health_code
        WHEN EXISTS (
            SELECT 1
            FROM automation_schedule_occurrences scheduled
            JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
            WHERE scheduled.automation_id = routines.id
              AND scheduled.kind = 'scheduled' AND scheduled.run_id IS NULL
              AND occurrence.state = 'failed'
        ) THEN 'migrated_schedule_occurrence_failed'
        ELSE schedule_health_code
    END,
    schedule_health_message = CASE WHEN archived = 1 THEN
        schedule_health_message || COALESCE((
            SELECT char(10) || 'Pending occurrence: ' || NULLIF(occurrence.diagnostic, '')
            FROM automation_schedule_occurrences scheduled
            JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
            WHERE scheduled.automation_id = routines.id
              AND scheduled.kind = 'scheduled' AND scheduled.run_id IS NULL
              AND occurrence.state = 'failed'
            LIMIT 1
        ), '')
    ELSE COALESCE((
            SELECT NULLIF(occurrence.diagnostic, '')
            FROM automation_schedule_occurrences scheduled
            JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
            WHERE scheduled.automation_id = routines.id
              AND scheduled.kind = 'scheduled' AND scheduled.run_id IS NULL
              AND occurrence.state = 'failed'
            LIMIT 1
        ), schedule_health_message)
    END
WHERE EXISTS (
    SELECT 1
    FROM automation_schedule_occurrences scheduled
    JOIN automation_occurrences occurrence ON occurrence.id = scheduled.occurrence_id
    WHERE scheduled.automation_id = routines.id
      AND scheduled.kind = 'scheduled'
      AND occurrence.state IN ('pending', 'dispatching', 'failed')
      AND scheduled.run_id IS NULL
);

-- Bounded, hidden history containers prevent large Task histories from
-- consuming the operator-authored Routine limit.
INSERT INTO routines(
    id, name, name_key, prompt, runtime, timeout_seconds,
    concurrency_limit, generation, archived, migration_only, schedule_enabled,
    schedule_health_status, created_at, updated_at
)
SELECT '00000000-0000-4000-8000-000000000101', 'Migrated manual history',
       '__migration_manual_history__', '', 'codex', 1, 10, 1, 1, 1, 0,
       'disabled', COALESCE(MIN(created_at), 0), COALESCE(MAX(created_at), 0)
FROM tasks
WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.task_id = tasks.id)
  AND NOT EXISTS (SELECT 1 FROM automation_occurrences WHERE automation_occurrences.task_id = tasks.id)
  AND tasks.workflow_id IS NULL
HAVING COUNT(*) > 0;

INSERT INTO routines(
    id, name, name_key, prompt, runtime, timeout_seconds,
    concurrency_limit, generation, archived, migration_only, schedule_enabled,
    schedule_health_status, created_at, updated_at
)
SELECT '00000000-0000-4000-8000-000000000103', 'Migrated workflow history',
       '__migration_workflow_history__', '', 'codex', 1, 10, 1, 1, 1, 0,
       'disabled', COALESCE(MIN(created_at), 0), COALESCE(MAX(created_at), 0)
FROM tasks
WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.task_id = tasks.id)
  AND NOT EXISTS (SELECT 1 FROM automation_occurrences WHERE automation_occurrences.task_id = tasks.id)
  AND tasks.workflow_id IS NOT NULL
HAVING COUNT(*) > 0;

INSERT INTO routines(
    id, name, name_key, prompt, runtime, timeout_seconds,
    concurrency_limit, generation, archived, migration_only, schedule_enabled,
    schedule_health_status, created_at, updated_at
)
SELECT '00000000-0000-4000-8000-000000000102', 'Migrated provider history',
       '__migration_provider_history__', '', 'codex', 1, 10, 1, 1, 1, 0,
       'disabled', COALESCE(MIN(task.created_at), 0), COALESCE(MAX(task.created_at), 0)
FROM tasks task
WHERE EXISTS (SELECT 1 FROM automation_occurrences WHERE automation_occurrences.task_id = task.id)
  AND NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.task_id = task.id)
HAVING COUNT(*) > 0;

-- Runs and Jobs map directly to Work and Targets while preserving IDs.
INSERT INTO work(
    id, request_key, request_digest, routine_id, routine_snapshot, source, scheduled_at,
    provider_snapshot, admitted_at, updated_at, terminal_at
)
SELECT
    run.id,
    'legacy-run:' || run.id,
    CAST('legacy-run:' || run.id AS BLOB),
    run.definition_id,
    json_object(
        'id', run.definition_id,
        'name', json_extract(run.definition_snapshot, '$.name'),
        'prompt', json_extract(run.definition_snapshot, '$.prompt'),
        'runtime', json_extract(run.definition_snapshot, '$.runtime'),
        'legacy_allowed_tools', json(json_extract(run.definition_snapshot, '$.allowed_tools')),
        'timeout_seconds', json_extract(run.definition_snapshot, '$.timeout_seconds'),
        'concurrency_limit', run.concurrency_limit,
        'generation', json_extract(run.definition_snapshot, '$.generation'),
        'repository_ids', json_group_array(job.repository_id),
        'legacy_run_request_key', run.request_key,
        'legacy_schedule_occurrence_id', (
            SELECT scheduled.occurrence_id FROM automation_schedule_occurrences scheduled
            WHERE scheduled.run_id = run.id LIMIT 1
        ),
        'legacy_schedule_kind', (
            SELECT scheduled.kind FROM automation_schedule_occurrences scheduled
            WHERE scheduled.run_id = run.id LIMIT 1
        ),
        'legacy_schedule_run_request_key', (
            SELECT scheduled.run_request_key FROM automation_schedule_occurrences scheduled
            WHERE scheduled.run_id = run.id LIMIT 1
        ),
        'cron', (
            SELECT scheduled.cron FROM automation_schedule_occurrences scheduled
            WHERE scheduled.run_id = run.id LIMIT 1
        ),
        'timezone', (
            SELECT scheduled.timezone FROM automation_schedule_occurrences scheduled
            WHERE scheduled.run_id = run.id LIMIT 1
        )
    ),
    CASE run.source_kind WHEN 'webhook' THEN 'provider_history' ELSE run.source_kind END,
    (
        SELECT scheduled.scheduled_at
        FROM automation_schedule_occurrences scheduled
        WHERE scheduled.run_id = run.id
        LIMIT 1
    ),
    CASE WHEN run.source_kind = 'webhook' THEN (
        SELECT json_object(
            'kind', automation.trigger_type,
            'automation_id', webhook.automation_id,
            'automation_version', occurrence.automation_version,
            'occurrence_id', webhook.occurrence_id,
            'delivery_id', webhook.delivery_id,
            'event', webhook.event,
            'action', webhook.action,
            'pull_request_number', webhook.pull_request_number,
            'pull_request_url', webhook.pull_request_url,
            'pull_request_title', webhook.pull_request_title,
            'base_branch', webhook.base_branch,
            'head_commit', webhook.head_commit,
            'definition_id', webhook.definition_id,
            'definition_snapshot', json(webhook.definition_snapshot),
            'parameters', json(webhook.parameters_json)
        )
        FROM automation_github_webhook_occurrences webhook
        JOIN automations automation ON automation.id = webhook.automation_id
        JOIN automation_occurrences occurrence ON occurrence.id = webhook.occurrence_id
        WHERE webhook.run_id = run.id
        LIMIT 1
    ) END,
    run.admitted_at,
    run.updated_at,
    CASE WHEN NOT EXISTS (
        SELECT 1 FROM jobs pending
        LEFT JOIN executions execution ON execution.id = pending.execution_id
        WHERE pending.run_id = run.id
          AND COALESCE(execution.state, pending.state) NOT IN ('succeeded', 'failed', 'cancelled')
    ) THEN run.updated_at END
FROM runs run
JOIN jobs job ON job.run_id = run.id
GROUP BY run.id;

INSERT INTO work_targets(
    id, work_id, repository_id, repository_identity, resolved_prompt,
    required_runtime, timeout_seconds, state, blocked_reason,
    assigned_worker_id, cancellation_requested, retry_may_repeat_effects,
    admitted_at, started_at, terminal_at, result, failure_reason
)
SELECT
    job.id,
    job.run_id,
    job.repository_id,
    job.repository_identity,
    COALESCE(NULLIF(run.resolved_prompt, ''), task.description, ''),
    COALESCE(execution.required_runtime, json_extract(run.definition_snapshot, '$.runtime'), 'codex'),
    COALESCE(task.timeout_seconds, json_extract(run.definition_snapshot, '$.timeout_seconds'), 3600),
    COALESCE(execution.state, job.state),
    CASE job.blocked_reason
        WHEN 'Waiting for an available Run concurrency slot.'
        THEN 'Waiting for an available Routine concurrency slot.'
        ELSE job.blocked_reason
    END,
    execution.assigned_worker_id,
    COALESCE(execution.cancellation_requested, job.state = 'cancelled'),
    COALESCE(execution.retry_count > 0, 0),
    job.admitted_at,
    (
        SELECT MIN(attempt.started_at) FROM attempts attempt
        WHERE attempt.execution_id = execution.id
    ),
    CASE WHEN COALESCE(execution.state, job.state) IN ('succeeded', 'failed', 'cancelled')
         THEN job.updated_at END,
    (
        SELECT attempt.result FROM attempts attempt
        WHERE attempt.execution_id = execution.id AND attempt.result IS NOT NULL
        ORDER BY attempt.attempt_number DESC LIMIT 1
    ),
    (
        SELECT attempt.error FROM attempts attempt
        WHERE attempt.execution_id = execution.id AND attempt.error IS NOT NULL
        ORDER BY attempt.attempt_number DESC LIMIT 1
    )
FROM jobs job
JOIN runs run ON run.id = job.run_id
LEFT JOIN tasks task ON task.id = job.task_id
LEFT JOIN executions execution ON execution.id = job.execution_id;

-- Standalone Task history becomes one Work with one Target. Exact settings stay
-- in the Work snapshot, not in the hidden history container.
INSERT INTO work(
    id, request_key, request_digest, routine_id, routine_snapshot, source, provider_snapshot,
    admitted_at, updated_at, terminal_at
)
SELECT
    task.id,
    'legacy-task:' || task.id,
    CAST('legacy-task:' || task.id AS BLOB),
    CASE
        WHEN occurrence.id IS NOT NULL THEN '00000000-0000-4000-8000-000000000102'
        WHEN task.workflow_id IS NOT NULL THEN '00000000-0000-4000-8000-000000000103'
        ELSE '00000000-0000-4000-8000-000000000101'
    END,
    json_object(
        'name', COALESCE(task.workflow_title, task.title),
        'prompt', COALESCE(occurrence.resolved_prompt, task.description),
        'runtime', execution.required_runtime,
        'legacy_allowed_tools', COALESCE(json_extract(task.definition_snapshot, '$.allowed_tools'), json_array()),
        'timeout_seconds', task.timeout_seconds,
        'concurrency_limit', 1,
        'generation', 1,
        'repository_ids', json_array(task.repository_id),
        'legacy_task_id', task.id,
        'legacy_task_request_key', task.request_key,
        'legacy_workflow_id', task.workflow_id,
        'legacy_workflow_revision_id', task.workflow_revision_id,
        'legacy_workflow_revision_number', task.workflow_revision_number,
        'legacy_workflow_title', task.workflow_title
    ),
    CASE WHEN occurrence.id IS NULL THEN 'manual' ELSE 'provider_history' END,
    CASE WHEN occurrence.id IS NOT NULL THEN json_object(
        'kind', automation.trigger_type,
        'automation_id', occurrence.automation_id,
        'automation_version', occurrence.automation_version,
        'occurrence_id', occurrence.id,
        'external_identity', occurrence.task_request_key,
        'issue', CASE WHEN issue.occurrence_id IS NOT NULL THEN json_object(
            'number', issue.issue_number,
            'url', issue.issue_url,
            'title', issue.issue_title,
            'state', issue.observed_state,
            'labels', json(issue.observed_labels_json),
            'configured_state', issue.configured_state,
            'required_labels', json(issue.required_labels_json)
        ) END,
        'pull_request', CASE WHEN pull_request.occurrence_id IS NOT NULL THEN json_object(
            'number', pull_request.pull_request_number,
            'url', pull_request.pull_request_url,
            'title', pull_request.pull_request_title,
            'state', pull_request.observed_state,
            'draft', json(CASE pull_request.observed_draft WHEN 1 THEN 'true' ELSE 'false' END),
            'base_branch', pull_request.observed_base_branch,
            'head_commit', pull_request.observed_head_commit,
            'labels', json(pull_request.observed_labels_json),
            'configured_state', pull_request.configured_state,
            'include_drafts', json(CASE pull_request.include_drafts WHEN 1 THEN 'true' ELSE 'false' END),
            'required_labels', json(pull_request.required_labels_json),
            'base_branches', json(pull_request.base_branches_json)
        ) END,
        'webhook', CASE WHEN webhook.occurrence_id IS NOT NULL THEN json_object(
            'delivery_id', webhook.delivery_id,
            'event', webhook.event,
            'action', webhook.action,
            'pull_request_number', webhook.pull_request_number,
            'pull_request_url', webhook.pull_request_url,
            'pull_request_title', webhook.pull_request_title,
            'base_branch', webhook.base_branch,
            'head_commit', webhook.head_commit,
            'definition_id', webhook.definition_id,
            'definition_snapshot', json(webhook.definition_snapshot),
            'parameters', json(webhook.parameters_json),
            'run_id', webhook.run_id
        ) END,
        'schedule', CASE WHEN scheduled.occurrence_id IS NOT NULL THEN json_object(
            'kind', scheduled.kind,
            'scheduled_at', scheduled.scheduled_at,
            'cron', scheduled.cron,
            'timezone', scheduled.timezone
        ) END
    ) END,
    task.created_at,
    execution.updated_at,
    CASE WHEN execution.state IN ('succeeded', 'failed', 'cancelled') THEN execution.updated_at END
FROM tasks task
JOIN executions execution ON execution.task_id = task.id
LEFT JOIN automation_occurrences occurrence ON occurrence.task_id = task.id
LEFT JOIN automations automation ON automation.id = occurrence.automation_id
LEFT JOIN automation_github_issue_occurrences issue ON issue.occurrence_id = occurrence.id
LEFT JOIN automation_github_pull_request_occurrences pull_request ON pull_request.occurrence_id = occurrence.id
LEFT JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
LEFT JOIN automation_schedule_occurrences scheduled ON scheduled.occurrence_id = occurrence.id
WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.task_id = task.id);

INSERT INTO work_targets(
    id, work_id, repository_id, repository_identity, resolved_prompt,
    required_runtime, timeout_seconds, state, assigned_worker_id,
    cancellation_requested, retry_may_repeat_effects, admitted_at, started_at,
    terminal_at, result, failure_reason
)
SELECT
    task.id,
    task.id,
    task.repository_id,
    repository.remote_identity,
    COALESCE(occurrence.resolved_prompt, task.description),
    execution.required_runtime,
    task.timeout_seconds,
    execution.state,
    execution.assigned_worker_id,
    execution.cancellation_requested,
    execution.retry_count > 0,
    task.created_at,
    (SELECT MIN(started_at) FROM attempts WHERE attempts.execution_id = execution.id),
    CASE WHEN execution.state IN ('succeeded', 'failed', 'cancelled') THEN execution.updated_at END,
    (SELECT result FROM attempts WHERE attempts.execution_id = execution.id AND result IS NOT NULL ORDER BY attempt_number DESC LIMIT 1),
    (SELECT error FROM attempts WHERE attempts.execution_id = execution.id AND error IS NOT NULL ORDER BY attempt_number DESC LIMIT 1)
FROM tasks task
JOIN executions execution ON execution.task_id = task.id
JOIN repositories repository ON repository.id = task.repository_id
LEFT JOIN automation_occurrences occurrence ON occurrence.task_id = task.id
WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.task_id = task.id);

CREATE TABLE executions_new (
    id TEXT PRIMARY KEY,
    work_target_id TEXT NOT NULL UNIQUE REFERENCES work_targets(id),
    assigned_worker_id TEXT NOT NULL REFERENCES workers(id),
    required_runtime TEXT NOT NULL CHECK (required_runtime IN ('pi', 'codex', 'claude-code')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'preparing', 'running', 'succeeded', 'failed', 'cancelled')),
    cancellation_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancellation_requested IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0)
);

INSERT INTO executions_new(
    id, work_target_id, assigned_worker_id, required_runtime, state,
    cancellation_requested, created_at, updated_at, retry_count
)
SELECT
    execution.id,
    COALESCE(job.id, execution.task_id),
    execution.assigned_worker_id,
    execution.required_runtime,
    execution.state,
    execution.cancellation_requested,
    execution.created_at,
    execution.updated_at,
    execution.retry_count
FROM executions execution
LEFT JOIN jobs job ON job.execution_id = execution.id;

DROP TABLE executions;
ALTER TABLE executions_new RENAME TO executions;

CREATE INDEX executions_claim_order ON executions(assigned_worker_id, state, created_at, id);

DROP TABLE product_upgrade_schedule_update_replays;
DROP TABLE product_model_upgrades;
DROP TABLE legacy_poller_observations;
DROP TABLE legacy_poller_imports;
DROP TABLE legacy_poller_migrations;
DROP TABLE automation_github_webhook_occurrences;
DROP TABLE automation_github_webhook_triggers;
DROP TABLE github_webhook_deliveries;
DROP TABLE automation_github_pull_request_occurrences;
DROP TABLE automation_github_pull_request_triggers;
DROP TABLE automation_github_issue_occurrences;
DROP TABLE automation_github_issue_triggers;
DROP TABLE automation_schedule_occurrences;
DROP TABLE automation_schedule_repositories;
DROP TABLE automation_schedule_triggers;
DROP TABLE automation_occurrences;
DROP TABLE automations;
DROP TABLE definition_mutations;
DROP TABLE jobs;
DROP TABLE runs;
DROP TABLE tasks;
DROP TABLE definitions;
DROP TABLE workflow_revisions;
DROP TABLE workflows;
DROP TABLE routines_migration_guard;
