-- +migrate Up
-- Tracks which on-disk files Bindery has pushed to Grimmory's BookDrop inbox,
-- making both the post-import hook and the bulk "Push library" sync idempotent
-- (#826). Grimmory's bookdrop endpoint performs no server-side dedup, so a
-- re-push would create a duplicate review entry over there — this table is the
-- only thing standing between a re-run sync and a BookDrop full of copies.
-- file_path is the library path at push time (book_files.path or the legacy
-- books.file_path column). grimmory_book_id is Grimmory's id when the upload
-- response carried one, 0 when the file went to the review queue without an id.
-- NOTE: the runner's historical "semicolon in a comment" gotcha was fixed in
-- #1465 — the splitter is now comment- and literal-aware. Trigger bodies
-- (BEGIN ... END) are still unsupported.
CREATE TABLE IF NOT EXISTS grimmory_pushes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id INTEGER NOT NULL,
    file_path TEXT NOT NULL UNIQUE,
    grimmory_book_id INTEGER NOT NULL DEFAULT 0,
    pushed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_grimmory_pushes_book ON grimmory_pushes(book_id);

-- +migrate Down
DROP TABLE IF EXISTS grimmory_pushes;
