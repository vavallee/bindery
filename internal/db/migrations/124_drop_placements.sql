-- +migrate Up
-- Which book last placed each file in an external drop folder (#3562).
--
-- A drop file has no book_files row, because the external tool owns it from the
-- moment it lands. Two books can render to the same name, so a handoff needs this
-- to tell an earlier drop of the same book, which it replaces, from another
-- book's file the tool has not picked up yet, which it must not touch. A row can
-- outlive its file once the tool consumes it. It is only read for a path that
-- exists, and the next placement there overwrites it.
-- NOTE: no semicolons inside comments, the migration runner splits on them.
CREATE TABLE IF NOT EXISTS drop_placements (
    path    TEXT    PRIMARY KEY,
    book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE
);

-- +migrate Down
DROP TABLE IF EXISTS drop_placements;
