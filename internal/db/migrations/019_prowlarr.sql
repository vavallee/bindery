-- Prowlarr integration: store configured Prowlarr instances and track
-- which Bindery indexers were created by a Prowlarr sync so they can be
-- updated/removed automatically without user intervention.

CREATE TABLE IF NOT EXISTS prowlarr_instances (
    id         INTEGER  PRIMARY KEY AUTOINCREMENT,
    name       TEXT     NOT NULL DEFAULT 'Prowlarr',
    url        TEXT     NOT NULL,
    api_key    TEXT     NOT NULL DEFAULT '',
    sync_on_startup INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER  NOT NULL DEFAULT 1,
    last_sync_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

ALTER TABLE indexers ADD COLUMN prowlarr_instance_id INTEGER REFERENCES prowlarr_instances(id) ON DELETE SET NULL;
ALTER TABLE indexers ADD COLUMN prowlarr_indexer_id   INTEGER;

CREATE INDEX IF NOT EXISTS idx_indexers_prowlarr_instance ON indexers(prowlarr_instance_id);
