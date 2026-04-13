-- +migrate Up

-- Rebuild `downloads` to add `ON DELETE SET NULL` on every foreign key.
-- The original schema used bare `REFERENCES` clauses (default = NO ACTION),
-- so deleting a referenced parent row — most visibly, an Author, which
-- cascades to Books — was blocked by a FK violation on the dangling
-- downloads row. Matching `history.book_id` (which already uses SET NULL),
-- we keep the download row around but detach it when its parent goes away.
-- Audit / history value outlives the book/indexer/client.
--
-- SQLite can't alter a FK constraint in place, so: disable FKs for the
-- swap, copy rows into a correctly-shaped new table, drop the old one,
-- rename, re-create indexes, re-enable FKs.

PRAGMA foreign_keys=OFF;

CREATE TABLE downloads_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    guid               TEXT    NOT NULL UNIQUE,
    book_id            INTEGER REFERENCES books(id)             ON DELETE SET NULL,
    edition_id         INTEGER REFERENCES editions(id)          ON DELETE SET NULL,
    indexer_id         INTEGER REFERENCES indexers(id)          ON DELETE SET NULL,
    download_client_id INTEGER REFERENCES download_clients(id)  ON DELETE SET NULL,
    title              TEXT    NOT NULL,
    nzb_url            TEXT    NOT NULL,
    size               INTEGER NOT NULL DEFAULT 0,
    sabnzbd_nzo_id     TEXT,
    status             TEXT    NOT NULL DEFAULT 'queued',
    protocol           TEXT    NOT NULL DEFAULT 'usenet',
    quality            TEXT    NOT NULL DEFAULT '',
    indexer_flags      TEXT    NOT NULL DEFAULT '{}',
    error_message      TEXT    NOT NULL DEFAULT '',
    added_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    grabbed_at         DATETIME,
    completed_at       DATETIME,
    imported_at        DATETIME
);

INSERT INTO downloads_new
SELECT id, guid, book_id, edition_id, indexer_id, download_client_id,
       title, nzb_url, size, sabnzbd_nzo_id, status, protocol, quality,
       indexer_flags, error_message, added_at, grabbed_at, completed_at,
       imported_at
FROM downloads;

DROP TABLE downloads;
ALTER TABLE downloads_new RENAME TO downloads;

CREATE INDEX idx_downloads_status         ON downloads(status);
CREATE INDEX idx_downloads_book_id        ON downloads(book_id);
CREATE INDEX idx_downloads_sabnzbd_nzo_id ON downloads(sabnzbd_nzo_id);

PRAGMA foreign_keys=ON;
