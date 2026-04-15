-- +migrate Up

-- Calibre integration gains a per-library mode selector. v0.8.0 shipped a
-- single boolean (calibre.enabled) driving the `calibredb add` flow. v0.8.1
-- adds a second flow (drop-folder ingest) and the UI lets the operator pick
-- which one to use, or leave the integration off entirely.
--
-- calibre.mode values:
--   off          no Calibre call on import (new default)
--   calibredb    shell out to `calibredb add --with-library` (v0.8.0 path)
--   drop_folder  write the file into a watched directory and poll Calibre
--                metadata.db for the resulting book id
--
-- calibre.drop_folder_path is the watch directory Calibre folder-watch
-- is pointed at. Empty when drop-folder mode is not in use.
INSERT OR IGNORE INTO settings (key, value) VALUES
    ('calibre.mode',              'off'),
    ('calibre.drop_folder_path',  '');

-- Migrate existing v0.8.0 installs: if the operator had the old boolean
-- toggle set to true, preserve their working setup by defaulting the new
-- mode to calibredb. Fresh installs (where calibre.enabled defaulted to
-- false) stay on off. We scope the UPDATE to value=off so an operator
-- who already picked a mode out-of-band is not overwritten.
UPDATE settings
SET value = 'calibredb'
WHERE key = 'calibre.mode'
  AND value = 'off'
  AND EXISTS (
      SELECT 1 FROM settings
      WHERE key = 'calibre.enabled' AND LOWER(value) = 'true'
  );

-- +migrate Down
-- Settings-row seeds are harmless if they linger, leave them in place on
-- rollback. Downgrading the binary will simply ignore the new keys.
