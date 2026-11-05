-- +migrate Up

-- Key pending releases on the book as well as the release (#3564).
--
-- guid was UNIQUE on its own, and the upsert resolved a conflict on guid
-- without touching book_id. A release held back for one book therefore could
-- not be held for a second book that wanted it too (the same work as two rows,
-- or two accounts each with their own copy): the second book's entry folded
-- into the first, so that book never listed it and never grabbed it when the
-- delay ran out. Each book now keeps its own row.
--
-- SQLite cannot drop a column UNIQUE constraint, so the table is rebuilt.
-- Nothing references pending_releases, so foreign keys need no special care.
-- Every existing row is unique on guid alone, so it is unique on the pair
-- too.

CREATE TABLE pending_releases_new (
    id           INTEGER PRIMARY KEY,
    book_id      INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    indexer_id   INTEGER,
    guid         TEXT NOT NULL,
    protocol     TEXT NOT NULL,
    size         INTEGER,
    age_minutes  INTEGER,
    quality      TEXT,
    custom_score INTEGER NOT NULL DEFAULT 0,
    reason       TEXT NOT NULL,
    first_seen   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    release_json TEXT NOT NULL,
    media_type   TEXT NOT NULL DEFAULT 'ebook',
    UNIQUE (guid, book_id)
);

INSERT INTO pending_releases_new
    (id, book_id, title, indexer_id, guid, protocol, size, age_minutes, quality,
     custom_score, reason, first_seen, release_json, media_type)
SELECT id, book_id, title, indexer_id, guid, protocol, size, age_minutes, quality,
       custom_score, reason, first_seen, release_json, media_type
FROM pending_releases;

DROP TABLE pending_releases;
ALTER TABLE pending_releases_new RENAME TO pending_releases;

-- The unique key leads on guid, so the per book reads and deletes still need
-- 107's idx_pending_releases_book_id, which went with the old table.
CREATE INDEX IF NOT EXISTS idx_pending_releases_book_id
    ON pending_releases(book_id);

-- +migrate Down

-- Back to one row per release. Where several books hold the same release the
-- oldest row is kept, which is the row the old upsert would have kept.

CREATE TABLE pending_releases_old (
    id           INTEGER PRIMARY KEY,
    book_id      INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    indexer_id   INTEGER,
    guid         TEXT NOT NULL UNIQUE,
    protocol     TEXT NOT NULL,
    size         INTEGER,
    age_minutes  INTEGER,
    quality      TEXT,
    custom_score INTEGER NOT NULL DEFAULT 0,
    reason       TEXT NOT NULL,
    first_seen   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    release_json TEXT NOT NULL,
    media_type   TEXT NOT NULL DEFAULT 'ebook'
);

INSERT INTO pending_releases_old
    (id, book_id, title, indexer_id, guid, protocol, size, age_minutes, quality,
     custom_score, reason, first_seen, release_json, media_type)
SELECT id, book_id, title, indexer_id, guid, protocol, size, age_minutes, quality,
       custom_score, reason, first_seen, release_json, media_type
FROM pending_releases
WHERE id IN (SELECT MIN(id) FROM pending_releases GROUP BY guid);

DROP TABLE pending_releases;
ALTER TABLE pending_releases_old RENAME TO pending_releases;

CREATE INDEX IF NOT EXISTS idx_pending_releases_book_id
    ON pending_releases(book_id);
