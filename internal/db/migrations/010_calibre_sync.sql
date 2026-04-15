-- +migrate Up

-- Calibre library read side. v0.8.0 introduced the write path
-- (calibredb add after import). v0.8.1 adds the ability to ingest an
-- existing Calibre library into Bindery on demand or at startup.
--
-- sync_on_startup: if true, main.go kicks off a background import the
-- moment the server boots. Default false so existing installs do not
-- get a surprise workload the next time they pull latest.
--
-- last_import_at: stamped by the importer on success as an RFC3339
-- string. The UI shows it next to the Import button. An empty value
-- means "never imported". Stored as settings rows because the
-- calibre.* namespace already exists and the Settings UI picks them
-- up with no extra plumbing.
INSERT OR IGNORE INTO settings (key, value) VALUES
    ('calibre.sync_on_startup', 'false'),
    ('calibre.last_import_at',  '');

-- +migrate Down
-- Rows are harmless if they linger. No destructive down step.
