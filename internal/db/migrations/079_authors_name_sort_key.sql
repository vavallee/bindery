-- Accent-folded ordering key for the Authors display-name sort (#2102).
--
-- sort_key (migration 058) folds sort_name ("Weir, Andy"), which fixed the
-- surname-first A–Z list (#1347) but cannot order the list by display name
-- ("Andy Weir"). Store the sibling key folded from `name` with the same Go
-- folder (authorSortKey), maintained on write and re-canonicalized by the
-- startup backfill. SQLite cannot fold accents, so existing rows are left at
-- '' here and populated by backfillAuthorSortKeys on the next startup.
ALTER TABLE authors ADD COLUMN name_sort_key TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_authors_name_sort_key ON authors(name_sort_key);
