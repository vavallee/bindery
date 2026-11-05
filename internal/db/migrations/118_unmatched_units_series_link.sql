-- +migrate Up

-- An optional series membership made while adopting an unmatched library
-- unit. It is stored alongside the other adoption side effects so Undo and
-- abandoned-claim recovery can remove only the membership this adoption
-- created, without disturbing later user edits.
ALTER TABLE unmatched_units ADD COLUMN series_link_json TEXT NOT NULL DEFAULT '';

-- +migrate Down

ALTER TABLE unmatched_units DROP COLUMN series_link_json;
