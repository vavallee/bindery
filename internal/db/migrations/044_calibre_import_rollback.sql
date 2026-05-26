-- +migrate Up
-- Calibre import/sync rollback (#643). Reporter hit metadata damage from a
-- bad Calibre import and had to ZFS-restore the whole catalogue.
-- This mirrors the ABS run-tracking, provenance, and snapshot shape so a
-- single bad library import can be unwound from the UI instead of backups.

CREATE TABLE calibre_import_runs (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id          TEXT     NOT NULL DEFAULT 'default',
    library_path       TEXT     NOT NULL DEFAULT '',
    status             TEXT     NOT NULL DEFAULT 'running',
    dry_run            INTEGER  NOT NULL DEFAULT 0,
    source_config_json TEXT     NOT NULL DEFAULT '{}',
    summary_json       TEXT     NOT NULL DEFAULT '{}',
    started_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at        DATETIME
);

CREATE INDEX idx_calibre_import_runs_started ON calibre_import_runs(started_at DESC);

CREATE TABLE calibre_provenance (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id     TEXT     NOT NULL DEFAULT 'default',
    entity_type   TEXT     NOT NULL CHECK(entity_type IN ('author', 'book', 'edition')),
    external_id   TEXT     NOT NULL,
    local_id      INTEGER  NOT NULL,
    import_run_id INTEGER  REFERENCES calibre_import_runs(id) ON DELETE SET NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (source_id, entity_type, external_id)
);

CREATE INDEX idx_calibre_provenance_local ON calibre_provenance(entity_type, local_id);

CREATE TABLE calibre_entity_snapshots (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        INTEGER  NOT NULL REFERENCES calibre_import_runs(id) ON DELETE CASCADE,
    source_id     TEXT     NOT NULL DEFAULT 'default',
    entity_type   TEXT     NOT NULL CHECK(entity_type IN ('author', 'book', 'edition')),
    external_id   TEXT     NOT NULL,
    local_id      INTEGER  NOT NULL DEFAULT 0,
    outcome       TEXT     NOT NULL DEFAULT '',
    metadata_json TEXT     NOT NULL DEFAULT '{}',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (run_id, entity_type, external_id, local_id)
);

CREATE INDEX idx_calibre_entity_snapshots_run ON calibre_entity_snapshots(run_id, entity_type, local_id);

-- +migrate Down

DROP INDEX IF EXISTS idx_calibre_entity_snapshots_run;
DROP TABLE IF EXISTS calibre_entity_snapshots;
DROP INDEX IF EXISTS idx_calibre_provenance_local;
DROP TABLE IF EXISTS calibre_provenance;
DROP INDEX IF EXISTS idx_calibre_import_runs_started;
DROP TABLE IF EXISTS calibre_import_runs;
