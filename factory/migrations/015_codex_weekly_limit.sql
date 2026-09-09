ALTER TABLE workers ADD COLUMN weekly_limit_used_percent INTEGER
CHECK (weekly_limit_used_percent BETWEEN 0 AND 100);

ALTER TABLE workers ADD COLUMN weekly_limit_resets_at INTEGER;
