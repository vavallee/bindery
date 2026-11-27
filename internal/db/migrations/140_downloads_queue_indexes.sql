-- +migrate Up
-- GET /queue, which an open Queue page polls every 5 seconds, lists every
-- download that is not imported plus the imports of the last 7 days
-- (DownloadRepo.ListActive). Its one statement walked idx_downloads_added_at
-- and fetched every row of the table to test it, so the poll cost grew with
-- the imports it was about to skip: 9,943 rows fetched to return 3,214.
--
-- These two indexes let each half of that list find its own rows. The first is
-- partial on the same predicate the query writes, so it holds only the rows
-- that are not imported, already in the order the list is returned in. The
-- second finds the recent imports by the same expression the query compares.
CREATE INDEX IF NOT EXISTS idx_downloads_not_imported
    ON downloads(added_at)
    WHERE status <> 'imported';

CREATE INDEX IF NOT EXISTS idx_downloads_status_finished
    ON downloads(status, COALESCE(imported_at, completed_at, added_at));
