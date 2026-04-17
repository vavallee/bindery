CREATE TABLE IF NOT EXISTS pending_releases (
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
    release_json TEXT NOT NULL
);
