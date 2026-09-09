CREATE INDEX executions_metrics_created
ON executions(created_at);

CREATE INDEX executions_metrics_outcomes
ON executions(state, updated_at);
