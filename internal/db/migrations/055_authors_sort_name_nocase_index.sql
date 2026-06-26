-- +migrate Up
-- The Authors list sorts A-Z / Z-A by sort_name with COLLATE NOCASE (see
-- db/authors.go authorSortOrder). The existing idx_authors_sort_name from
-- migration 048 uses the column's default BINARY collation, so SQLite cannot
-- use it to satisfy a NOCASE-ordered query and falls back to a full in-memory
-- sort on every page. This adds a matching NOCASE index so the paginated
-- ORDER BY stays index-backed.
--
-- IF NOT EXISTS so reruns are safe even after an operator hand-added it.
CREATE INDEX IF NOT EXISTS idx_authors_sort_name_nocase ON authors(sort_name COLLATE NOCASE);

-- +migrate Down
DROP INDEX IF EXISTS idx_authors_sort_name_nocase;
