-- +migrate Up
-- Wave 2 / Bundle E indexes for the columns that the paginated List
-- endpoints sort by. Without these the books/authors/history list queries
-- do a full in-memory sort on every page request, which dominates wall
-- time on 50k-book Calibre imports.
--
-- IF NOT EXISTS on every CREATE so reruns are safe even after an operator
-- has hand-added one of these on a hot database.
--
-- Naming note. An ASCending idx_history_created_at already exists from
-- migration 001. SQLite cannot use an ASC index for a DESC scan walk in
-- reverse without an extra OrderBy step, so we add a dedicated DESC-keyed
-- index (suffix `_desc`) to power the newest-first list query in
-- HistoryRepo.

-- Books. List sorts by sort_title. Status-filtered list also sorts by
-- sort_title. ListByAuthor and ListByAuthorAndUser sort by release_date.
-- release_date is nullable. SQLite default sort order treats NULL as the
-- smallest value, so an ASC sort puts NULL rows first and a DESC sort
-- puts them last. The list endpoint is ASC by release_date today.
-- Callers that need a strict NULLS LAST contract should add
-- `ORDER BY release_date IS NULL, release_date` in the query (this index
-- still supports the rewritten form).
CREATE INDEX IF NOT EXISTS idx_books_sort_title         ON books(sort_title);
CREATE INDEX IF NOT EXISTS idx_books_release_date       ON books(release_date);
CREATE INDEX IF NOT EXISTS idx_books_status_sort_title  ON books(status, sort_title);

-- Authors. List sorts by sort_name.
CREATE INDEX IF NOT EXISTS idx_authors_sort_name        ON authors(sort_name);

-- series_books has a composite PK (series_id, book_id) which only indexes
-- forward lookups by series_id. Reverse lookups `WHERE book_id = ?` (used
-- by series.GetSeriesIDsForBook and ListBookSeriesByAuthor) currently
-- scan the whole join table. A standalone book_id index turns those into
-- a single index seek.
CREATE INDEX IF NOT EXISTS idx_series_books_book        ON series_books(book_id);

-- History pagination index. The list query is ORDER BY created_at DESC.
-- The existing idx_history_created_at (migration 001) is ASC-only and
-- SQLite still has to walk it backwards plus pay an extra OrderBy when
-- the result is paginated. A DESC-keyed index removes that overhead.
CREATE INDEX IF NOT EXISTS idx_history_created_at_desc  ON history(created_at DESC);

-- +migrate Down

DROP INDEX IF EXISTS idx_history_created_at_desc;
DROP INDEX IF EXISTS idx_series_books_book;
DROP INDEX IF EXISTS idx_authors_sort_name;
DROP INDEX IF EXISTS idx_books_status_sort_title;
DROP INDEX IF EXISTS idx_books_release_date;
DROP INDEX IF EXISTS idx_books_sort_title;
