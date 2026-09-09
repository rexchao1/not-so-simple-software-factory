CREATE INDEX jobs_metrics_admitted
ON jobs(admitted_at, run_id, repository_id);

CREATE INDEX runs_metrics_definition
ON runs(definition_id, admitted_at);
