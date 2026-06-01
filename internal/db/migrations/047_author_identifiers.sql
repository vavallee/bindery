-- +migrate Up
-- General author-provider identity map. authors.foreign_id remains the primary
-- identity, while this table keeps every known upstream/import ID attached to
-- the same canonical author row.
CREATE TABLE IF NOT EXISTS author_identifiers (
    author_id  INTEGER NOT NULL,
    provider   TEXT    NOT NULL,
    foreign_id TEXT    NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (author_id, foreign_id),
    FOREIGN KEY (author_id) REFERENCES authors(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_author_identifiers_author_id ON author_identifiers(author_id);
CREATE INDEX IF NOT EXISTS idx_author_identifiers_provider ON author_identifiers(provider);

INSERT OR IGNORE INTO author_identifiers (author_id, provider, foreign_id, created_at, updated_at)
SELECT
    id,
    CASE
        WHEN LOWER(foreign_id) LIKE 'gb:%' THEN 'googlebooks'
        WHEN LOWER(foreign_id) LIKE 'hc:%' THEN 'hardcover'
        WHEN LOWER(foreign_id) LIKE 'dnb:%' THEN 'dnb'
        WHEN LOWER(foreign_id) LIKE 'calibre:%' THEN 'calibre'
        WHEN LOWER(foreign_id) LIKE 'abs:%' THEN 'audiobookshelf'
        ELSE 'openlibrary'
    END,
    foreign_id,
    created_at,
    updated_at
FROM authors
WHERE TRIM(foreign_id) != '';

-- +migrate Down

DROP TABLE IF EXISTS author_identifiers;
