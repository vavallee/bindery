-- +migrate Up
-- General book-provider identity map (#1705), modelled on author_identifiers
-- (migration 052).
--
-- books.foreign_id is a single column carrying one provider's id, with the
-- provider encoded in the prefix ("hc:" for Hardcover, a bare "OL...W" for
-- OpenLibrary, "calibre:", "abs:"). Authors got a second identity table in 052
-- precisely because one column cannot hold "this row is both OL123W and
-- hc:456"; books never did, and that asymmetry is what #1705 is.
--
-- The reported sequence: a Hardcover-linked series fills its books, the user
-- runs Find better metadata on the author and picks their OpenLibrary entry,
-- then refreshes. relinkExistingAuthorToUpstream rewrites the AUTHOR's
-- foreign_id and keeps the old one in author_identifiers, but the books keep
-- their "hc:" ids. The next sync fetches OpenLibrary works, the exact-id lookup
-- misses every one of them, and the title fallback misses whenever the two
-- providers disagree about the title. So the same volume is created a second
-- time and the user is left with two rows for volume 1, and no way to merge
-- them that does not cost them the series link.
--
-- With this table an id from any provider resolves to the book that already
-- carries it, so the second row is never minted.
--
-- foreign_id is globally UNIQUE for the same reason it is on author_identifiers:
-- one upstream id names one book. The uniqueness is what makes the lookup a
-- single indexed probe rather than a scan.
CREATE TABLE IF NOT EXISTS book_identifiers (
    book_id    INTEGER NOT NULL,
    provider   TEXT    NOT NULL,
    foreign_id TEXT    NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (book_id, foreign_id),
    FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_book_identifiers_book_id ON book_identifiers(book_id);
CREATE INDEX IF NOT EXISTS idx_book_identifiers_provider ON book_identifiers(provider);

-- Seed every existing book's primary id, so the new lookup is never blind to a
-- book that predates this table. Mirrors 052's backfill, including its provider
-- classification.
INSERT OR IGNORE INTO book_identifiers (book_id, provider, foreign_id, created_at, updated_at)
SELECT
    id,
    CASE
        WHEN LOWER(foreign_id) LIKE 'gb:%' THEN 'googlebooks'
        WHEN LOWER(foreign_id) LIKE 'hc:%' THEN 'hardcover'
        WHEN LOWER(foreign_id) LIKE 'dnb:%' THEN 'dnb'
        WHEN LOWER(foreign_id) LIKE 'calibre:%' THEN 'calibre'
        WHEN LOWER(foreign_id) LIKE 'abs:%' THEN 'audiobookshelf'
        ELSE 'openlibrary'
    END,
    foreign_id,
    created_at,
    updated_at
FROM books
WHERE TRIM(foreign_id) != '';

-- +migrate Down

DROP TABLE IF EXISTS book_identifiers;
