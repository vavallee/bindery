-- Accent-folded sort key for the Authors A–Z/Z–A list (#1347, follow-up to #1312).
--
-- #1312 added `COLLATE NOCASE`, which folds ASCII case only. SQLite has no
-- Unicode-aware collation here, so any sort_name beginning with a diacritic
-- (Ö, Á, Ł, Ø, Æ…) still sorted after "Z". We store an accent-folded, lowercased
-- key computed in Go (see authorSortKey) and ORDER BY it with a plain BINARY
-- index. SQLite cannot fold accents, so existing rows are left at '' here and
-- populated by the Go-side backfillAuthorSortKeys pass on the next startup.
ALTER TABLE authors ADD COLUMN sort_key TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_authors_sort_key ON authors(sort_key);
