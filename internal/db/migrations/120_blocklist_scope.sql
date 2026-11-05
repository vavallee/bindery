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
-- the file did not match its metadata, or a user blocklisted a grab or an
-- import of it from that book's History) is scope 'book' and only blocks
-- that book. A user blocklisting a failed or stalled download from History is
-- saying the release is broken, so that entry is 'release'.
--
-- An entry is matched for a book when its scope is 'release', or when it is
-- scoped to that book. A 'book' entry whose book was deleted (book_id is ON
-- DELETE SET NULL) blocks nothing.
--
-- Existing rows: the download client's verdicts are recognised by the reason
-- prefixes their writers have always used ('downloadFailed: ' from the import
-- poller, 'stalled: ' from the stall handler) and become 'release', as do
-- rows blocklisted from a failed or stalled download's History event (reason
-- exactly 'downloadFailed' or 'downloadStalled', the event type the History
-- action stores), since that action now writes release entries. Rows with
-- no book (Readarr imports, and rows whose book was deleted and NULLed) also
-- become 'release', and so do rows whose book_id names a book that no longer
-- exists (left behind while foreign keys were off), so nothing that blocked a
-- release before this migration stops blocking it because it lost its book.
-- Everything else is 'book'.
ALTER TABLE blocklist ADD COLUMN scope TEXT NOT NULL DEFAULT 'book';

UPDATE blocklist
SET scope = 'release'
WHERE book_id IS NULL
   OR book_id NOT IN (SELECT id FROM books)
   OR reason LIKE 'downloadFailed: %'
   OR reason LIKE 'stalled: %'
   OR reason IN ('downloadFailed', 'downloadStalled');

-- +migrate Down

ALTER TABLE blocklist DROP COLUMN scope;
