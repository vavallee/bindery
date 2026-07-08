-- The bookCTE (see internal/db/books.go) materialises the first book_files row
-- per format for every book on every list/get/lookup query, with
-- WHERE format = 'ebook' GROUP BY book_id (and again for 'audiobook'). The only
-- index on book_files was idx_book_files_book_id(book_id), so the format
-- predicate could not be satisfied from an index and each of those two CTE arms
-- scanned the whole table -- cost that grows with total file count and runs on
-- the single SQLite connection everything else waits behind.
-- This composite index lets both arms seek straight to a format and walk it in
-- (book_id, id) order, so the GROUP BY / MIN(id) is index-driven.
-- NOTE: no semicolons inside comments -- the migration runner splits on them.
CREATE INDEX IF NOT EXISTS idx_book_files_format_book ON book_files(format, book_id, id);

-- +migrate Down

DROP INDEX IF EXISTS idx_book_files_format_book;
