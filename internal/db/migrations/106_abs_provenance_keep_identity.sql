-- +migrate Up
-- An ABS item linked to a book through a file that book already tracked
-- (#1691) did not make that book. The book keeps its own author and title on
-- every later import, not only the first, so the link records how it was made.
-- Existing rows were all made from the item itself and keep the old behaviour.
ALTER TABLE abs_provenance ADD COLUMN keep_identity INTEGER NOT NULL DEFAULT 0;

-- +migrate Down

ALTER TABLE abs_provenance DROP COLUMN keep_identity;
