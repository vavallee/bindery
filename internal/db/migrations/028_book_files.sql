-- +migrate Up

-- book_files tracks every on-disk file associated with a book.
-- Replaces the single ebook_file_path / audiobook_file_path columns on books
-- so that multi-file downloads (epub + mobi + pdf) are all recorded.
CREATE TABLE book_files (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id    INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    format     TEXT    NOT NULL CHECK(format IN ('ebook', 'audiobook')),
    path       TEXT    NOT NULL UNIQUE,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_book_files_book_id ON book_files(book_id);

-- Backfill from existing ebook_file_path column.
INSERT OR IGNORE INTO book_files (book_id, format, path, created_at)
SELECT id, 'ebook', ebook_file_path, CURRENT_TIMESTAMP
FROM books
WHERE ebook_file_path IS NOT NULL AND ebook_file_path != '';

-- Backfill from existing audiobook_file_path column (when distinct).
INSERT OR IGNORE INTO book_files (book_id, format, path, created_at)
SELECT id, 'audiobook', audiobook_file_path, CURRENT_TIMESTAMP
FROM books
WHERE audiobook_file_path IS NOT NULL AND audiobook_file_path != '';

-- Backfill from legacy file_path for books that only have the old column.
INSERT OR IGNORE INTO book_files (book_id, format, path, created_at)
SELECT id, 'ebook', file_path, CURRENT_TIMESTAMP
FROM books
WHERE file_path IS NOT NULL AND file_path != ''
  AND (ebook_file_path IS NULL OR ebook_file_path = '')
  AND (audiobook_file_path IS NULL OR audiobook_file_path = '');

-- +migrate Down

DROP INDEX IF EXISTS idx_book_files_book_id;
DROP TABLE IF EXISTS book_files;
