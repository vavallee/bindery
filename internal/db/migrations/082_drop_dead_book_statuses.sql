-- +migrate Up
-- Rewrite the two book statuses Bindery has removed (#2374).
--
-- 'downloading' and 'downloaded' were declared in the model and accepted by
-- PUT /api/v1/book/{id}, but no code path in Bindery ever wrote them: the only
-- writers are refreshBookStatus (imported/wanted, derived from book_files),
-- MarkWantedMonitored (wanted) and the skip action (skipped). Download progress
-- lives in the downloads table under its own state machine and was never
-- projected onto books.status.
--
-- So a row holding one of these values can only have come from outside: a
-- script or a third-party client driving the book update endpoint by hand. The
-- endpoint no longer accepts them, and every reader that tested for them is
-- gone, which would leave such a row rendering an unknown status pill and
-- sitting on no list at all.
--
-- 'wanted' is the right landing spot rather than 'imported': both values meant
-- "acquisition started, no file yet", and refreshBookStatus will flip the row
-- to 'imported' by itself the moment a book_files row exists. Monitored is
-- deliberately left alone; whether Bindery pursues the book is a separate
-- decision the user owns.
--
-- Idempotent: re-running matches nothing.
UPDATE books
SET status = 'wanted', updated_at = CURRENT_TIMESTAMP
WHERE status IN ('downloading', 'downloaded');

-- +migrate Down
-- Irreversible: the rows this rewrote are indistinguishable from books that
-- were already 'wanted', and both source values are no longer valid anywhere
-- in the schema or the API.
