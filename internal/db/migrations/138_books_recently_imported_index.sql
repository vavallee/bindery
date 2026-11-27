-- +migrate Up
-- The OPDS "Recently Added" feed shows the 50 most recently updated books a
-- reader can download. It used to load every imported book and sort them in
-- Go, so the request grew with the library (596 ms at 40,000 books). This
-- index holds exactly the rows that feed can show, newest first, so
-- BookRepo.ListRecentDownloadable reads the top of it and stops.
--
-- It is partial on purpose. Bindery never runs ANALYZE, so an unrestricted
-- index on updated_at could be picked up by unrelated queries; the planner
-- only considers this one for a query that repeats the whole WHERE clause,
-- and the one query that does names it with INDEXED BY (without that the
-- planner prefers idx_books_status_sort_title for the status equality and
-- sorts the whole status in a temp b-tree). Keep the WHERE clause here and in
-- ListRecentDownloadable identical.
CREATE INDEX IF NOT EXISTS idx_books_recently_imported
    ON books(updated_at DESC, id DESC)
    WHERE status = 'imported' AND monitored = 1 AND excluded = 0 AND file_path != '';

-- +migrate Down

DROP INDEX IF EXISTS idx_books_recently_imported;
