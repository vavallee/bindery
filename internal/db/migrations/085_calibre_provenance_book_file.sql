-- +migrate Up

-- Allow 'book_file' as a Calibre run-tracking entity type (#1635).
--
-- #2195 made the Calibre import register a book_files row for every format
-- Calibre reports, which fixed the tracked path never being written. It did
-- not record provenance for those rows, so they sit outside the rollback
-- machinery migration 044 exists to provide: rolling a run back leaves its
-- file rows behind.
--
-- For a book the run CREATED this is already covered by accident, because
-- book_files.book_id is ON DELETE CASCADE (028) and foreign_keys is on for
-- every connection (see connectionPragmaDSN). The gap is a book that already
-- existed and gained a Calibre path during the run: rollback restores the
-- book's snapshot and the new file row survives.
--
-- Same rebuild shape as 072, which widened these two CHECKs for 'series_link'.
-- SQLite cannot ALTER a CHECK constraint directly.

PRAGMA foreign_keys = OFF;

CREATE TABLE calibre_provenance_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id     TEXT     NOT NULL DEFAULT 'default',
    entity_type   TEXT     NOT NULL CHECK(entity_type IN ('author', 'book', 'edition', 'series_link', 'book_file')),
    external_id   TEXT     NOT NULL,
    local_id      INTEGER  NOT NULL,
    import_run_id INTEGER  REFERENCES calibre_import_runs(id) ON DELETE SET NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (source_id, entity_type, external_id)
);

INSERT INTO calibre_provenance_new (id, source_id, entity_type, external_id, local_id, import_run_id, created_at, updated_at)
    SELECT id, source_id, entity_type, external_id, local_id, import_run_id, created_at, updated_at
    FROM calibre_provenance;

DROP TABLE calibre_provenance;
ALTER TABLE calibre_provenance_new RENAME TO calibre_provenance;

CREATE INDEX idx_calibre_provenance_local ON calibre_provenance(entity_type, local_id);

CREATE TABLE calibre_entity_snapshots_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        INTEGER  NOT NULL REFERENCES calibre_import_runs(id) ON DELETE CASCADE,
    source_id     TEXT     NOT NULL DEFAULT 'default',
    entity_type   TEXT     NOT NULL CHECK(entity_type IN ('author', 'book', 'edition', 'series_link', 'book_file')),
    external_id   TEXT     NOT NULL,
    local_id      INTEGER  NOT NULL DEFAULT 0,
    outcome       TEXT     NOT NULL DEFAULT '',
    metadata_json TEXT     NOT NULL DEFAULT '{}',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (run_id, entity_type, external_id, local_id)
);

INSERT INTO calibre_entity_snapshots_new (id, run_id, source_id, entity_type, external_id, local_id, outcome, metadata_json, created_at)
    SELECT id, run_id, source_id, entity_type, external_id, local_id, outcome, metadata_json, created_at
    FROM calibre_entity_snapshots;

DROP TABLE calibre_entity_snapshots;
ALTER TABLE calibre_entity_snapshots_new RENAME TO calibre_entity_snapshots;

CREATE INDEX idx_calibre_entity_snapshots_run ON calibre_entity_snapshots(run_id, entity_type, local_id);

PRAGMA foreign_keys = ON;

-- +migrate Down

-- Drop the book-file rows first: they cannot satisfy the narrowed constraint,
-- and keeping them would abort the rebuild. Same order 072's Down uses.

DELETE FROM calibre_entity_snapshots WHERE entity_type = 'book_file';
DELETE FROM calibre_provenance WHERE entity_type = 'book_file';

PRAGMA foreign_keys = OFF;

CREATE TABLE calibre_provenance_old (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id     TEXT     NOT NULL DEFAULT 'default',
    entity_type   TEXT     NOT NULL CHECK(entity_type IN ('author', 'book', 'edition', 'series_link')),
    external_id   TEXT     NOT NULL,
    local_id      INTEGER  NOT NULL,
    import_run_id INTEGER  REFERENCES calibre_import_runs(id) ON DELETE SET NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (source_id, entity_type, external_id)
);

INSERT INTO calibre_provenance_old (id, source_id, entity_type, external_id, local_id, import_run_id, created_at, updated_at)
    SELECT id, source_id, entity_type, external_id, local_id, import_run_id, created_at, updated_at
    FROM calibre_provenance;

DROP TABLE calibre_provenance;
ALTER TABLE calibre_provenance_old RENAME TO calibre_provenance;

CREATE INDEX idx_calibre_provenance_local ON calibre_provenance(entity_type, local_id);

CREATE TABLE calibre_entity_snapshots_old (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        INTEGER  NOT NULL REFERENCES calibre_import_runs(id) ON DELETE CASCADE,
    source_id     TEXT     NOT NULL DEFAULT 'default',
    entity_type   TEXT     NOT NULL CHECK(entity_type IN ('author', 'book', 'edition', 'series_link')),
    external_id   TEXT     NOT NULL,
    local_id      INTEGER  NOT NULL DEFAULT 0,
    outcome       TEXT     NOT NULL DEFAULT '',
    metadata_json TEXT     NOT NULL DEFAULT '{}',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (run_id, entity_type, external_id, local_id)
);

INSERT INTO calibre_entity_snapshots_old (id, run_id, source_id, entity_type, external_id, local_id, outcome, metadata_json, created_at)
    SELECT id, run_id, source_id, entity_type, external_id, local_id, outcome, metadata_json, created_at
    FROM calibre_entity_snapshots;

DROP TABLE calibre_entity_snapshots;
ALTER TABLE calibre_entity_snapshots_old RENAME TO calibre_entity_snapshots;

CREATE INDEX idx_calibre_entity_snapshots_run ON calibre_entity_snapshots(run_id, entity_type, local_id);

PRAGMA foreign_keys = ON;
