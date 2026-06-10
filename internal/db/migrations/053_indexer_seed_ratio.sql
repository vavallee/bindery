-- +migrate Up
-- Per-indexer seed-ratio override (#883). Nullable: NULL means "no override"
-- so the torrent inherits the download client's global ratio rule. A stored
-- value of -1 is the unlimited sentinel (Prowlarr/qBittorrent convention),
-- carried verbatim through to the client adapters which translate it per their
-- own API mechanics.
ALTER TABLE indexers ADD COLUMN seed_ratio REAL;

-- +migrate Down

ALTER TABLE indexers DROP COLUMN seed_ratio;
