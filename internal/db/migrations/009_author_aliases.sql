-- +migrate Up

-- Author aliases: alternate names ("RR Haywood", "R.R. Haywood", "R R Haywood")
-- that resolve to a single canonical author row. Created when two duplicate
-- authors are merged, and consulted during the add-author flow so the same
-- human isn't ingested twice under a punctuation variant.
--
-- author_id points at the canonical row. Deleting the canonical author
-- cascades the aliases away (they have no meaning without a target).
-- name is globally unique so a single alias cannot point at two authors.
-- source_ol_id is optional: when the alias came from a merged-away author
-- we keep the OpenLibrary id it had, so future lookups by that id land on
-- the canonical row.

CREATE TABLE author_aliases (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    author_id     INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    name          TEXT    NOT NULL UNIQUE,
    source_ol_id  TEXT,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_author_aliases_author_id    ON author_aliases(author_id);
CREATE INDEX idx_author_aliases_source_ol_id ON author_aliases(source_ol_id);

-- +migrate Down

DROP TABLE IF EXISTS author_aliases;
