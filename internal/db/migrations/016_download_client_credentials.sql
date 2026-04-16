-- +migrate Up

-- Add dedicated credential columns for downloader clients that authenticate
-- with username/password (qBittorrent, Transmission).
ALTER TABLE download_clients ADD COLUMN username TEXT NOT NULL DEFAULT '';
ALTER TABLE download_clients ADD COLUMN password TEXT NOT NULL DEFAULT '';

-- Backfill from legacy storage where credentials were temporarily multiplexed
-- into url_base/api_key.
UPDATE download_clients
SET username = url_base
WHERE type IN ('qbittorrent', 'transmission')
  AND TRIM(username) = ''
  AND TRIM(url_base) != '';

UPDATE download_clients
SET password = api_key
WHERE type IN ('qbittorrent', 'transmission')
  AND password = ''
  AND api_key != '';

-- +migrate Down
-- SQLite cannot drop columns without rebuilding the table; keep columns.
