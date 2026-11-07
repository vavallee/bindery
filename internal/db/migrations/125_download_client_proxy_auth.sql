-- +migrate Up
-- Optional HTTP basic auth for a download client that sits behind a
-- reverse proxy (a seedbox, for example) which wants credentials before the
-- request reaches the client. Separate from the client's own username and
-- password. Empty means no basic auth.
ALTER TABLE download_clients ADD COLUMN proxy_username TEXT NOT NULL DEFAULT '';
ALTER TABLE download_clients ADD COLUMN proxy_password TEXT NOT NULL DEFAULT '';
