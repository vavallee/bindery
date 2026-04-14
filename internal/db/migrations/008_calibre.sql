-- +migrate Up

-- Calibre integration. Adds an optional stable pointer from a Bindery book
-- row to the corresponding Calibre book id so post-import sync and future
-- OPDS lookups can map between libraries without re-matching on title.
--
-- calibre_id is nullable: Calibre integration is opt-in, so pre-existing
-- and newly-imported rows stay at NULL until the importer receives an id
-- from `calibredb add`. A UNIQUE index would be wrong — two Bindery rows
-- pointing at the same Calibre id is not impossible if the user manually
-- re-imports — so we only index for lookup speed.
ALTER TABLE books ADD COLUMN calibre_id INTEGER;
CREATE INDEX idx_books_calibre_id ON books(calibre_id) WHERE calibre_id IS NOT NULL;

-- Seed default Calibre settings rows so the settings endpoint exposes them
-- in its List output before the user has touched the Settings UI. Using
-- INSERT OR IGNORE lets the migration run cleanly on fresh DBs and on any
-- upgrade where an operator pre-populated the keys out-of-band.
INSERT OR IGNORE INTO settings (key, value) VALUES
    ('calibre.enabled',      'false'),
    ('calibre.library_path', ''),
    ('calibre.binary_path',  '');

-- +migrate Down
DROP INDEX IF EXISTS idx_books_calibre_id;
-- SQLite cannot drop columns without a table rebuild; leave the column in
-- place on rollback. The seeded settings rows are harmless if they linger.
