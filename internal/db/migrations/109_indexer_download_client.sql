-- +migrate Up
-- Per-indexer download client pin (#3080).
--
-- download_client_id names the one client every grab from this indexer must go
-- to, so a private tracker's releases never reach the public client. NULL, which
-- is what every existing row gets on upgrade, keeps today's routing unchanged.
--
-- ON DELETE RESTRICT is the backstop for the API's own refusal (409 naming the
-- indexers): the database must never be allowed to quietly turn a pinned
-- indexer back into an unpinned one, because that is exactly the fallback this
-- column exists to prevent.
-- NOTE: the migration runner splits on semicolons, so keep them out of comments.
ALTER TABLE indexers ADD COLUMN download_client_id INTEGER REFERENCES download_clients(id) ON DELETE RESTRICT;

-- +migrate Down
-- SQLite before 3.35 cannot DROP COLUMN. download_client_id is additive and NULL
-- already reads as "no pin" to every reader, so leaving it in place is a no-op.
