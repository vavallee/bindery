-- +migrate Up
-- Attribution for an author photo or bio filled in by an author enricher
-- (#3287). Each column holds a small JSON object (provider, page URL, credit,
-- licence) or the empty string, which is what every existing row gets and what
-- a value from the author's own primary provider keeps.
ALTER TABLE authors ADD COLUMN description_source TEXT NOT NULL DEFAULT '';
ALTER TABLE authors ADD COLUMN image_source TEXT NOT NULL DEFAULT '';

-- +migrate Down
-- SQLite before 3.35 cannot DROP COLUMN. Both columns are additive and the
-- empty string already reads as "no credit", so leaving them is a no-op.
