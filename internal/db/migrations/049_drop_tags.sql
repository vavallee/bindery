-- +migrate Up

-- D4a: remove the dormant tag surface (audit finding).
--
-- The tags feature has been carried in the schema since migration 003 but
-- never wired into any business logic. TagRepo.SetAuthorTags and
-- GetAuthorTags have zero non-test call sites. The frontend defines
-- listTags / addTag / deleteTag in web/src/api/client.ts but no
-- component invokes them. No indexer, searcher, downloader, scheduler,
-- importer, OPDS, recommender, or telemetry code consumes a tag id. The
-- indexers.tags and download_clients.tags JSON columns are never read or
-- written by application code.
--
-- Locking the surface into a per-user shape now (the original D4 plan)
-- forecloses future redesign for zero benefit. Delete it instead.
--
-- ALTER TABLE DROP COLUMN is supported by SQLite 3.35 and later. The
-- bundled modernc.org/sqlite driver ships a newer engine than that, so
-- the bare ALTERs below are safe and avoid the table-rebuild dance.
--
-- author_tags has FK CASCADEs to authors and tags. Both go away in this
-- migration so the ordering (children before parents) does not actually
-- matter, but DROPing author_tags first keeps the intent obvious.
--
-- Note. The migration runner splits on the statement terminator before
-- stripping comment lines, so this comment block must avoid that
-- character entirely.

DROP TABLE IF EXISTS author_tags;
DROP TABLE IF EXISTS tags;

ALTER TABLE indexers          DROP COLUMN tags;
ALTER TABLE download_clients  DROP COLUMN tags;

-- +migrate Down

-- Recreate the dormant surface exactly as migration 003 left it, so a
-- downgrade restores a schema-compatible (if still dormant) state.

CREATE TABLE tags (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT    NOT NULL UNIQUE
);

CREATE TABLE author_tags (
    author_id INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    tag_id    INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (author_id, tag_id)
);

ALTER TABLE indexers          ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
ALTER TABLE download_clients  ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
