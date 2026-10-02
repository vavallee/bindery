-- +migrate Up
-- Per-indexer seed time limits (#2206), alongside the seed_ratio override from
-- migration 053. Both are whole minutes and nullable: NULL means "no override",
-- so the torrent keeps the download client's own rule.
--   seed_time_minutes           stop seeding after this long in total.
--   inactive_seed_time_minutes  stop seeding after this long with no upload.
-- Which client honours which limit is decided in the downloader adapters, not
-- here: qBittorrent takes both, Transmission only the inactive one.
--
-- seed_time_source is the provenance of seed_time_minutes, with the same values
-- and rules as seed_ratio_source (migration 054): '' unset, 'prowlarr'
-- auto-populated from Prowlarr's torrentBaseSettings.seedTime, 'user' set or
-- cleared by hand and never touched by the syncer again. It is its own column
-- rather than a reuse of seed_ratio_source because most existing rows already
-- carry 'user' there from any edit of the indexer, which would stop Prowlarr
-- from ever filling the new value. No row has a seed time yet, so every row
-- starts unset. Prowlarr has no inactive seed time setting, so that column has
-- no provenance and is only ever written by the user.
ALTER TABLE indexers ADD COLUMN seed_time_minutes INTEGER;
ALTER TABLE indexers ADD COLUMN seed_time_source TEXT NOT NULL DEFAULT '';
ALTER TABLE indexers ADD COLUMN inactive_seed_time_minutes INTEGER;

-- +migrate Down

ALTER TABLE indexers DROP COLUMN inactive_seed_time_minutes;
ALTER TABLE indexers DROP COLUMN seed_time_source;
ALTER TABLE indexers DROP COLUMN seed_time_minutes;
