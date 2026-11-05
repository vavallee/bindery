-- +migrate Up

-- Scope each blocklist entry to its book or to the release (#3563).
--
-- Search matched the blocklist on the GUID alone, so an entry filed for one
-- book blocked that release for every book. With two accounts that meant one
-- user's blocklist decided what another user's book could grab, even when the
-- other book's quality profile allowed the format that got the release
-- rejected.
--
-- Migration 050 kept the blocklist global on purpose, for a release that is
-- broken for everyone: a broken NZB, a failed repair, a torrent the client
-- reports stalled. Those stay global, as scope 'release'. An entry that is a
-- judgement about one book (its profile refused the format or the language,
-- the file did not match its metadata, or a user blocklisted it from that
-- book's history) is scope 'book' and only blocks that book.
--
-- An entry is matched for a book when its scope is 'release', or when it is
-- scoped to that book. A 'book' entry whose book was deleted (book_id is ON
-- DELETE SET NULL) blocks nothing.
--
-- Existing rows: the download client's verdicts are recognised by the reason
-- prefixes their writers have always used ('downloadFailed: ' from the import
-- poller, 'stalled: ' from the stall handler) and become 'release'. Rows with
-- no book (Readarr imports, and rows whose book is gone) also become
-- 'release', so nothing that blocked a release before this migration stops
-- blocking it because it lost its book. Everything else is 'book'.
ALTER TABLE blocklist ADD COLUMN scope TEXT NOT NULL DEFAULT 'book';

UPDATE blocklist
SET scope = 'release'
WHERE book_id IS NULL
   OR reason LIKE 'downloadFailed: %'
   OR reason LIKE 'stalled: %';

-- +migrate Down

ALTER TABLE blocklist DROP COLUMN scope;
