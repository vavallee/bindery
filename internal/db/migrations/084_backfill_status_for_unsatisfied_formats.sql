-- +migrate Up
-- Repair books stuck at 'imported' while a monitored format has no file (#1634).
--
-- books.status is derived from media_type plus the on-disk paths, and
-- refreshBookStatus keeps it correct on every book_files write. Nothing kept it
-- correct when media_type changed on its own. A single-format book that was
-- already imported and then widened to 'both' by Hardcover list sync or by
-- edition hydration kept status 'imported' while gaining a monitored format
-- with nothing behind it.
--
-- That state is invisible rather than merely wrong. The wanted page
-- (ListByStatusAndUser), the scheduled sweep (wantedSearchQueue) and the author
-- bulk search all select on status alone, so the missing format is never
-- searched and the library row looks entirely normal. #2096 fixed one producer
-- of the state in v1.32.2 (author refresh) but shipped no repair, and its
-- reporter had 29 books widened in a single run before that.
--
-- The predicate is the same one models.Book.ReevaluateStatus applies, using the
-- same effective-path expression as bookColumns: the stored column first, then
-- the first book_files row for the format. The fallback matters because
-- migration 028 leaves books whose book_files row exists while the scalar
-- column is still empty; reading the column alone would move those rows to
-- 'wanted' when their file is present.
--
-- 'skipped' is left alone, it encodes a user decision. 'wanted' rows are
-- already correct. Excluded books are included: status is a fact about the
-- book, and the sweep skips excluded rows on its own.
--
-- Idempotent: after the update the matched rows are 'wanted', so a second run
-- matches nothing.
UPDATE books
SET status = 'wanted', updated_at = CURRENT_TIMESTAMP
WHERE status = 'imported'
  AND (
        (media_type IN ('ebook', 'both')
         AND COALESCE(NULLIF(ebook_file_path, ''),
                      (SELECT path FROM book_files
                       WHERE book_id = books.id AND format = 'ebook'
                       ORDER BY id LIMIT 1),
                      '') = '')
     OR (media_type IN ('audiobook', 'both')
         AND COALESCE(NULLIF(audiobook_file_path, ''),
                      (SELECT path FROM book_files
                       WHERE book_id = books.id AND format = 'audiobook'
                       ORDER BY id LIMIT 1),
                      '') = '')
      );

-- +migrate Down
-- Irreversible: a row this rewrote is indistinguishable from a book that was
-- already 'wanted' with the same missing format.
