-- Folded search keys for the library search box (#1660).
--
-- Both halves of the Books and Authors search were `LIKE ? COLLATE NOCASE`
-- against the raw title/name. SQLite folds the 26 ASCII letters and nothing
-- else — in LIKE and in NOCASE alike (sqlite.org/datatype3.html) — so typing
-- "muller" never found "Müller", and a query typed on macOS (decomposed) never
-- found a row stored from a provider (composed). The same limitation was
-- already worked around for ORDERING in migration 058; this is the search half
-- of it.
--
-- We store the key folded in Go by textutil.FoldForSearch and match a
-- similarly folded query against it, so no Unicode-aware collation is needed at
-- query time. SQLite cannot fold, so existing rows are left at '' here and
-- populated by the Go-side backfillSearchKeys pass on the next startup.
--
-- No index on the search_key columns: the search is a substring match and
-- `LIKE '%q%'` can never use one (sqlite.org/optoverview.html). The column
-- still pays for itself, because it is shorter than the text it replaces and
-- is scanned without a collation callback.
ALTER TABLE books ADD COLUMN search_key TEXT NOT NULL DEFAULT '';
ALTER TABLE authors ADD COLUMN search_key TEXT NOT NULL DEFAULT '';
ALTER TABLE author_aliases ADD COLUMN search_key TEXT NOT NULL DEFAULT '';

-- Ordering key for the Books A–Z list, the sibling of authors.sort_key.
-- #1347 fixed the Authors list; the Books list kept ordering on the raw
-- sort_title, so a title starting with Ö, Á, Ł or Ø still sorted after "Z".
ALTER TABLE books ADD COLUMN sort_key TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_books_sort_key ON books(sort_key);
