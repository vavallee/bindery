-- +migrate Up
-- Per-client media-type eligibility. Both default to enabled so existing
-- clients keep receiving every grab exactly as before until a user opts a
-- client out of a media type.
ALTER TABLE download_clients ADD COLUMN enabled_for_books INTEGER NOT NULL DEFAULT 1;
ALTER TABLE download_clients ADD COLUMN enabled_for_audiobooks INTEGER NOT NULL DEFAULT 1;
