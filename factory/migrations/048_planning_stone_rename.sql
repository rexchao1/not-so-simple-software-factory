-- 048: the chunk inside a checkpoint is a stone, not a boulder.
--
-- One word was doing two jobs. A boulder is the whole idea a project plans,
-- too big for one spec, and there is one per project: it is the top card on
-- the Roadmap page and the thing the light factory routes into checkpoints.
-- Migration 046 then used the same word for the two to four groups a frozen
-- checkpoint's pebbles fall into, so "the boulder" meant either the project or
-- a piece of one rung of it depending on who was speaking. The inner one is
-- renamed to stone and the vocabulary closes: boulder, checkpoint, stone,
-- pebble, each naming exactly one level.
--
-- A pure rename, like migration 030. No row is created, dropped or rewritten,
-- and no data moves; only names change. The API's `stones` and `stone_id` keys
-- and the store's error codes are the same rename one layer up.
--
-- Refuse rather than lose data when the new name is already taken. A database
-- that already holds a planning_stones table did not get it from here, and
-- renaming onto it would put two shapes in one name. A BEFORE INSERT trigger
-- carries the refusal because it is the only way to raise a readable message.
CREATE TABLE planning_stone_rename_name_collision_guard (name TEXT PRIMARY KEY);

CREATE TRIGGER planning_stone_rename_name_collision_refuse
BEFORE INSERT ON planning_stone_rename_name_collision_guard
BEGIN
    SELECT RAISE(ABORT, 'migration 048 refused: a table named planning_stones already exists, and renaming planning_boulders onto it would lose data; rename or remove that table, then upgrade again');
END;

INSERT INTO planning_stone_rename_name_collision_guard(name)
SELECT name FROM sqlite_master
WHERE type = 'table' AND name = 'planning_stones';

-- Table before columns. Renaming the table first rewrites the foreign key in
-- planning_pebbles to point at planning_stones, and each column rename after
-- it carries through both halves of that key, so the child never spends a
-- statement pointing at a name that is not there.
ALTER TABLE planning_boulders RENAME TO planning_stones;
ALTER TABLE planning_stones RENAME COLUMN boulder_id TO stone_id;
ALTER TABLE planning_pebbles RENAME COLUMN boulder_id TO stone_id;

-- SQLite carries an index through a rename but keeps its old name, and an
-- index called planning_boulders_order on a table called planning_stones is a
-- trap for the next reader. Dropped and recreated, same columns, same order.
DROP INDEX planning_boulders_order;
DROP INDEX planning_pebbles_boulder;

CREATE INDEX planning_stones_order ON planning_stones(project, number, ordinal);
CREATE INDEX planning_pebbles_stone ON planning_pebbles(project, number, stone_id);

DROP TABLE planning_stone_rename_name_collision_guard;
