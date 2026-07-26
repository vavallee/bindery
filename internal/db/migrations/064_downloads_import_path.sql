-- +migrate Up
-- Persist the on-disk location of a completed download whose files could not be
-- matched to a book (#1589). The scanner used to discard this path on an
-- unmatched-import failure, so the only way to import the files later was an
-- async re-poll of the download client — impossible once the client forgot the
-- download, and invisible for downloads without a client. Storing it lets the
-- queue "Match to book" action import the already-downloaded files directly
-- against the book the user picks. Empty = no recorded path (the prior state).
ALTER TABLE downloads ADD COLUMN import_path TEXT NOT NULL DEFAULT '';
