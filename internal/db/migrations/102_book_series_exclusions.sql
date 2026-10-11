-- +migrate Up
-- A book the user took out of a series. Keyed by the series' provider foreign
-- id rather than its row, so the record holds when that series is merged away
-- (its id becomes an alias) or deleted and recreated by a later refresh.
-- Linking the book back by hand clears it, and so does unlocking all of the
-- book's fields. The position the book had is kept so it can be restored.
CREATE TABLE book_series_exclusions (
    book_id            INTEGER  NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    series_foreign_id  TEXT     NOT NULL,
    position_in_series TEXT     NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (book_id, series_foreign_id)
);

-- +migrate Down

DROP TABLE IF EXISTS book_series_exclusions;
