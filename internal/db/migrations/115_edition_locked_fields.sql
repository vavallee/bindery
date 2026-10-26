-- +migrate Up
-- Edition field locks. locked_fields is a JSON array of edition field names
-- ("publisher", "publishDate", "isbn13", "isbn10", "asin") that imports and
-- metadata hydration must not overwrite or fill, mirroring books.locked_fields
-- (migration 062). An empty array is the pre-migration behavior for every
-- existing row: nothing locked.
ALTER TABLE editions ADD COLUMN locked_fields TEXT NOT NULL DEFAULT '[]';
