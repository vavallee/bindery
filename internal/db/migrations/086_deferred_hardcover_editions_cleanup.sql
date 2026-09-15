-- Book-owned work must disappear on direct, bulk or cascaded deletion, and
-- survive transaction rollback along with its book.
CREATE TABLE deferred_hardcover_editions (
    book_id INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
    value   TEXT NOT NULL
);

-- Preserve live work from the former private-settings representation.
INSERT INTO deferred_hardcover_editions (book_id, value)
SELECT books.id, settings.value
FROM books JOIN settings
    ON settings.key = 'auth.hardcover_deferred_editions.' || books.id;

-- Includes orphan markers whose books were already deleted.
DELETE FROM settings WHERE key GLOB 'auth.hardcover_deferred_editions.*';
