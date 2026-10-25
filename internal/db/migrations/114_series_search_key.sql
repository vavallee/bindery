-- +migrate Up
-- Folded search key for series titles, so the Books search box can match a
-- book by the name of a series it belongs to (#3462). Same scheme as
-- migration 083: the key is folded in Go by textutil.FoldForSearch, so
-- existing rows start at '' and are filled by backfillSearchKeys.
-- Dropping the marker makes the next boot rerun that backfill, which already
-- ran at the current revision on an upgraded install and would otherwise
-- leave every existing series unsearchable.
-- NOTE: no semicolons inside comments, the migration runner splits on them.
ALTER TABLE series ADD COLUMN search_key TEXT NOT NULL DEFAULT '';
DELETE FROM settings WHERE key = 'backfill.search_key_rev';

-- +migrate Down
-- SQLite before 3.35 cannot DROP COLUMN. The column is additive, so leaving
-- it is a no-op.
