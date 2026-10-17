-- +migrate Up
-- Library adoption: record the target book's status prior to adoption (#2885).
-- When undoing an adoption into a Skipped book, this lets Undo restore it to
-- Skipped rather than leaving it Wanted.
ALTER TABLE unmatched_units ADD COLUMN prior_book_status TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE unmatched_units DROP COLUMN prior_book_status;
