-- +migrate Up

-- Calibre-Web-Automated (CWA) integration. CWA runs as a separate container
-- (https://github.com/crocodilestick/Calibre-Web-Automated) and watches a
-- shared "ingest" directory. Anything dropped there is automatically added
-- to its Calibre library and removed from the ingest folder afterwards.
--
-- bindery's role: when an ebook import succeeds, also drop a copy of the
-- final file into the configured CWA ingest directory. The copy is
-- best-effort. Failure is logged but does not roll back bindery's import.
-- We copy (not move) so bindery's own library stays intact regardless of
-- what CWA does with its consumed copy.
--
-- cwa.ingest_path empty disables the integration. Default is empty so the
-- migration is a no-op for users who don't care.
INSERT OR IGNORE INTO settings (key, value) VALUES
    ('cwa.ingest_path', '');

-- +migrate Down
-- Settings-row seeds are harmless if they linger, leave them in place on
-- rollback. Downgrading the binary will simply ignore the new key.
