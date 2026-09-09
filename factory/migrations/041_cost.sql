-- 041: attempt cost.
--
-- A Claude Code attempt reports an estimated dollar cost, the token usage of
-- its top-level loop, and a per-model breakdown that includes subagent
-- requests. The attempt keeps the sum over its stages and each stage keeps
-- its own, so the same six columns land on attempts and session_stages. Every
-- column is nullable and NULL means not measured: attempts that ran before
-- this migration, and Codex and Pi attempts, show nothing rather than zero.
-- Plain ADD COLUMN, no backfill, and no table rebuild.

ALTER TABLE attempts ADD COLUMN cost_usd REAL
  CHECK (cost_usd IS NULL OR cost_usd >= 0);
ALTER TABLE attempts ADD COLUMN input_tokens INTEGER
  CHECK (input_tokens IS NULL OR input_tokens >= 0);
ALTER TABLE attempts ADD COLUMN cache_creation_input_tokens INTEGER
  CHECK (cache_creation_input_tokens IS NULL OR cache_creation_input_tokens >= 0);
ALTER TABLE attempts ADD COLUMN cache_read_input_tokens INTEGER
  CHECK (cache_read_input_tokens IS NULL OR cache_read_input_tokens >= 0);
ALTER TABLE attempts ADD COLUMN output_tokens INTEGER
  CHECK (output_tokens IS NULL OR output_tokens >= 0);
ALTER TABLE attempts ADD COLUMN models TEXT
  CHECK (models IS NULL OR json_valid(models));

ALTER TABLE session_stages ADD COLUMN cost_usd REAL
  CHECK (cost_usd IS NULL OR cost_usd >= 0);
ALTER TABLE session_stages ADD COLUMN input_tokens INTEGER
  CHECK (input_tokens IS NULL OR input_tokens >= 0);
ALTER TABLE session_stages ADD COLUMN cache_creation_input_tokens INTEGER
  CHECK (cache_creation_input_tokens IS NULL OR cache_creation_input_tokens >= 0);
ALTER TABLE session_stages ADD COLUMN cache_read_input_tokens INTEGER
  CHECK (cache_read_input_tokens IS NULL OR cache_read_input_tokens >= 0);
ALTER TABLE session_stages ADD COLUMN output_tokens INTEGER
  CHECK (output_tokens IS NULL OR output_tokens >= 0);
ALTER TABLE session_stages ADD COLUMN models TEXT
  CHECK (models IS NULL OR json_valid(models));
