-- 049: a pebble built outside the factory.
--
-- A pebble's state used to come only from the Work row that built it, so a
-- task someone finished by hand and merged themselves read "planned" on the
-- Planning page forever, and the stone above it never finished either. These
-- two columns let a pebble be marked built directly, with the commit or pull
-- request that carries it.
--
-- Both nullable and both set together: almost every pebble still goes through
-- the factory, and a NULL built_at is how a row says this one is not one of
-- the exceptions. built_ref is not a foreign key or a shaped column, because
-- it holds either a commit SHA or a pull request URL and the roadmap only
-- ever displays it, never parses it.
--
-- A pebble that also has a matching Work row keeps that Work's state; the
-- join in roadmapStampPebble runs after this column is read and overwrites it
-- when a real run exists, because a real run is stronger evidence than a hand
-- entry.
ALTER TABLE planning_pebbles ADD COLUMN built_at INTEGER;
ALTER TABLE planning_pebbles ADD COLUMN built_ref TEXT;
