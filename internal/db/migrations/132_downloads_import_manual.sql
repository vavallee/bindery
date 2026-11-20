-- +migrate Up

-- Whether a download parked by drop folder pair gating (importHeld) was a
-- manual import (#3945). A held format is released later, under the context of
-- whatever import tick or sibling download triggers it, and the language and
-- content checks are never applied to a manual import. The importer therefore
-- has to read this off the held row, not off the releasing call. Only a held
-- row's value is read; the importer writes it every time it parks one. Rows
-- held before this migration read 0 and are checked like an automatic import.
ALTER TABLE downloads ADD COLUMN import_manual INTEGER NOT NULL DEFAULT 0;

-- +migrate Down

ALTER TABLE downloads DROP COLUMN import_manual;
