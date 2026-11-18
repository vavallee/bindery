-- +migrate Up

-- Options of the Audible Sync wishlist import list (internal/audiblesynclistsyncer).
--
-- skip_owned: leave a wishlist book alone when the Audible account already owns
-- it. Audible Sync downloads owned books itself, so searching an indexer for one
-- would fetch a second copy. On by default: it is what an operator running
-- Audible Sync expects.
--
-- remove_on_import: once Bindery has imported a wishlist book, take it off the
-- Audible wishlist. Off by default, because it writes to the operator's Audible
-- account.
--
-- Both are ignored by every other list type.
ALTER TABLE import_lists ADD COLUMN skip_owned INTEGER NOT NULL DEFAULT 1;
ALTER TABLE import_lists ADD COLUMN remove_on_import INTEGER NOT NULL DEFAULT 0;

-- Which books an Audible Sync list created, so a later sync can act on its own
-- books and nobody else's: the unavailable books list stops searching for a
-- book once Audible makes it downloadable again and Audible Sync fetches it,
-- and must not touch a book the operator wanted through another list.
-- A row goes when its list or its book does.
CREATE TABLE import_list_books (
    import_list_id INTEGER NOT NULL REFERENCES import_lists(id) ON DELETE CASCADE,
    book_id        INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    asin           TEXT    NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL,
    PRIMARY KEY (import_list_id, book_id)
);
CREATE INDEX idx_import_list_books_book ON import_list_books(book_id);

-- +migrate Down
DROP TABLE import_list_books;
ALTER TABLE import_lists DROP COLUMN remove_on_import;
ALTER TABLE import_lists DROP COLUMN skip_owned;
