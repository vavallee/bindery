-- +migrate Up

-- Key authors on (owner, provider id) instead of provider id alone (#3555).
--
-- authors.foreign_id was NOT NULL UNIQUE across the whole instance, and
-- author_identifiers.foreign_id the same. With BINDERY_ENFORCE_TENANCY on an
-- author row belongs to one account, so once alice added Frank Herbert, bob
-- could neither add him nor add any of his books: both answered 409. Each
-- account now holds its own row for the same author.
--
-- A NULL owner (a shared author, or one created by an import) counts as owner
-- 0 in the new key. SQLite treats NULLs as distinct in a UNIQUE index, so a
-- plain UNIQUE (owner_user_id, foreign_id) would let any number of shared rows
-- carry the same provider id. COALESCE closes that.
--
-- With tenancy off nothing changes in practice: AuthorRepo still refuses a
-- second author for a provider id anywhere in the instance (see
-- upsertIdentifierTx), so there is one row per author, as before.
--
-- SQLite cannot drop a column UNIQUE, so both tables are rebuilt. Foreign keys
-- are off for the rebuild (the runner takes the bare PRAGMA lines out of the
-- transaction and toggles them around it): dropping authors with them on
-- would cascade into books, aliases, identifiers and monitored series. The
-- runner compares foreign_key_check before and after and refuses to commit a
-- rebuild that added a violation.
--
-- The authors columns, defaults and indexes below are copied from a database
-- migrated to 129, not reconstructed from the earlier migrations. Every row
-- is copied, ids included, and the AUTOINCREMENT high water mark is carried
-- over so a deleted author's id is never handed out again.

PRAGMA foreign_keys = OFF;

CREATE TABLE authors_new (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    foreign_id               TEXT    NOT NULL,
    name                     TEXT    NOT NULL,
    sort_name                TEXT    NOT NULL,
    description              TEXT    NOT NULL DEFAULT '',
    image_url                TEXT    NOT NULL DEFAULT '',
    disambiguation           TEXT    NOT NULL DEFAULT '',
    ratings_count            INTEGER NOT NULL DEFAULT 0,
    average_rating           REAL    NOT NULL DEFAULT 0,
    monitored                INTEGER NOT NULL DEFAULT 1,
    quality_profile_id       INTEGER,
    root_folder_id           INTEGER,
    metadata_provider        TEXT    NOT NULL DEFAULT 'openlibrary',
    last_metadata_refresh_at DATETIME,
    created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata_profile_id      INTEGER,
    owner_user_id            INTEGER REFERENCES users(id),
    audiobook_root_folder_id INTEGER,
    monitor_mode             TEXT    NOT NULL DEFAULT 'all',
    monitor_latest_count     INTEGER NOT NULL DEFAULT 1,
    sort_key                 TEXT    NOT NULL DEFAULT '',
    monitor_new_items        TEXT    NOT NULL DEFAULT 'all',
    locked_fields            TEXT    NOT NULL DEFAULT '[]',
    catalogue_populated_at   TIMESTAMP,
    name_sort_key            TEXT    NOT NULL DEFAULT '',
    search_key               TEXT    NOT NULL DEFAULT '',
    last_discovery_at        DATETIME,
    description_source       TEXT    NOT NULL DEFAULT '',
    image_source             TEXT    NOT NULL DEFAULT '',
    last_enrichment_at       DATETIME
);

INSERT INTO authors_new
    (id, foreign_id, name, sort_name, description, image_url, disambiguation,
     ratings_count, average_rating, monitored, quality_profile_id, root_folder_id,
     metadata_provider, last_metadata_refresh_at, created_at, updated_at,
     metadata_profile_id, owner_user_id, audiobook_root_folder_id, monitor_mode,
     monitor_latest_count, sort_key, monitor_new_items, locked_fields,
     catalogue_populated_at, name_sort_key, search_key, last_discovery_at,
     description_source, image_source, last_enrichment_at)
SELECT id, foreign_id, name, sort_name, description, image_url, disambiguation,
       ratings_count, average_rating, monitored, quality_profile_id, root_folder_id,
       metadata_provider, last_metadata_refresh_at, created_at, updated_at,
       metadata_profile_id, owner_user_id, audiobook_root_folder_id, monitor_mode,
       monitor_latest_count, sort_key, monitor_new_items, locked_fields,
       catalogue_populated_at, name_sort_key, search_key, last_discovery_at,
       description_source, image_source, last_enrichment_at
FROM authors;

-- Carry the AUTOINCREMENT counter. The copy above set authors_new's counter to
-- the highest surviving id; the old one can be higher when the newest authors
-- were deleted. The rename below moves this row to the name authors.
INSERT INTO sqlite_sequence (name, seq)
SELECT 'authors_new', 0
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'authors_new');

UPDATE sqlite_sequence
SET seq = MAX(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'authors'), 0))
WHERE name = 'authors_new';

DROP TABLE authors;
ALTER TABLE authors_new RENAME TO authors;

CREATE INDEX idx_authors_foreign_id ON authors(foreign_id);
CREATE INDEX idx_authors_name ON authors(name);
CREATE INDEX idx_authors_owner ON authors(owner_user_id);
CREATE INDEX idx_authors_sort_name ON authors(sort_name);
CREATE INDEX idx_authors_sort_name_nocase ON authors(sort_name COLLATE NOCASE);
CREATE INDEX idx_authors_sort_key ON authors(sort_key);
CREATE INDEX idx_authors_name_sort_key ON authors(name_sort_key);
CREATE INDEX idx_authors_quality_profile_id ON authors(quality_profile_id);

-- The new identity. Every existing row is unique on foreign_id alone, so it
-- is unique on the pair too and this cannot fail on upgrade.
CREATE UNIQUE INDEX idx_authors_owner_foreign_id
    ON authors(COALESCE(owner_user_id, 0), foreign_id);

-- author_identifiers keeps its (author_id, foreign_id) primary key and loses
-- the instance wide UNIQUE on foreign_id, so bob's Herbert can carry the same
-- provider ids as alice's. AuthorRepo.upsertIdentifierTx keeps one holder per
-- id within an owner (and across the instance with tenancy off).

CREATE TABLE author_identifiers_new (
    author_id  INTEGER NOT NULL,
    provider   TEXT    NOT NULL,
    foreign_id TEXT    NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (author_id, foreign_id),
    FOREIGN KEY (author_id) REFERENCES authors(id) ON DELETE CASCADE
);

INSERT INTO author_identifiers_new (author_id, provider, foreign_id, created_at, updated_at)
SELECT author_id, provider, foreign_id, created_at, updated_at
FROM author_identifiers;

DROP TABLE author_identifiers;
ALTER TABLE author_identifiers_new RENAME TO author_identifiers;

CREATE INDEX idx_author_identifiers_author_id ON author_identifiers(author_id);
CREATE INDEX idx_author_identifiers_provider ON author_identifiers(provider);
-- Lookups by provider id used the UNIQUE's automatic index.
CREATE INDEX idx_author_identifiers_foreign_id ON author_identifiers(foreign_id);

PRAGMA foreign_keys = ON;

-- +migrate Down

-- Back to one row per provider id across the instance. If two accounts hold
-- their own row for the same author the copy fails on the UNIQUE and the
-- rollback stops, rather than deleting one account's author and orphaning
-- its books. Merge or delete the duplicates first.

PRAGMA foreign_keys = OFF;

CREATE TABLE authors_old (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    foreign_id               TEXT    NOT NULL UNIQUE,
    name                     TEXT    NOT NULL,
    sort_name                TEXT    NOT NULL,
    description              TEXT    NOT NULL DEFAULT '',
    image_url                TEXT    NOT NULL DEFAULT '',
    disambiguation           TEXT    NOT NULL DEFAULT '',
    ratings_count            INTEGER NOT NULL DEFAULT 0,
    average_rating           REAL    NOT NULL DEFAULT 0,
    monitored                INTEGER NOT NULL DEFAULT 1,
    quality_profile_id       INTEGER,
    root_folder_id           INTEGER,
    metadata_provider        TEXT    NOT NULL DEFAULT 'openlibrary',
    last_metadata_refresh_at DATETIME,
    created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata_profile_id      INTEGER,
    owner_user_id            INTEGER REFERENCES users(id),
    audiobook_root_folder_id INTEGER,
    monitor_mode             TEXT    NOT NULL DEFAULT 'all',
    monitor_latest_count     INTEGER NOT NULL DEFAULT 1,
    sort_key                 TEXT    NOT NULL DEFAULT '',
    monitor_new_items        TEXT    NOT NULL DEFAULT 'all',
    locked_fields            TEXT    NOT NULL DEFAULT '[]',
    catalogue_populated_at   TIMESTAMP,
    name_sort_key            TEXT    NOT NULL DEFAULT '',
    search_key               TEXT    NOT NULL DEFAULT '',
    last_discovery_at        DATETIME,
    description_source       TEXT    NOT NULL DEFAULT '',
    image_source             TEXT    NOT NULL DEFAULT '',
    last_enrichment_at       DATETIME
);

INSERT INTO authors_old SELECT * FROM authors;

DROP TABLE authors;
ALTER TABLE authors_old RENAME TO authors;

CREATE INDEX idx_authors_foreign_id ON authors(foreign_id);
CREATE INDEX idx_authors_name ON authors(name);
CREATE INDEX idx_authors_owner ON authors(owner_user_id);
CREATE INDEX idx_authors_sort_name ON authors(sort_name);
CREATE INDEX idx_authors_sort_name_nocase ON authors(sort_name COLLATE NOCASE);
CREATE INDEX idx_authors_sort_key ON authors(sort_key);
CREATE INDEX idx_authors_name_sort_key ON authors(name_sort_key);
CREATE INDEX idx_authors_quality_profile_id ON authors(quality_profile_id);

CREATE TABLE author_identifiers_old (
    author_id  INTEGER NOT NULL,
    provider   TEXT    NOT NULL,
    foreign_id TEXT    NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (author_id, foreign_id),
    FOREIGN KEY (author_id) REFERENCES authors(id) ON DELETE CASCADE
);

INSERT INTO author_identifiers_old SELECT * FROM author_identifiers;

DROP TABLE author_identifiers;
ALTER TABLE author_identifiers_old RENAME TO author_identifiers;

CREATE INDEX idx_author_identifiers_author_id ON author_identifiers(author_id);
CREATE INDEX idx_author_identifiers_provider ON author_identifiers(provider);

PRAGMA foreign_keys = ON;
