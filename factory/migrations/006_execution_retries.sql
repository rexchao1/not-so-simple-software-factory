ALTER TABLE executions
ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0
CHECK (retry_count >= 0);

UPDATE executions
SET retry_count = (
    SELECT CASE
        WHEN executions.state = 'queued'
             AND executions.updated_at > executions.created_at
        THEN MAX(1, COUNT(*))
        WHEN COUNT(*) > 1
        THEN COUNT(*) - 1
        ELSE 0
    END
    FROM attempts
    WHERE attempts.execution_id = executions.id
);
