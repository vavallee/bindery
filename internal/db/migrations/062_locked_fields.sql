-- +migrate Up
-- Manual metadata editing with field locks (#1237, #1446). locked_fields is a
-- JSON array of field names the user has edited by hand (e.g.
-- ["title","genres","language"]). Every metadata-refresh / enrichment /
-- merge path checks the set before overwriting, so a manual edit survives
-- the nightly refresh and author-works sync. Empty array = nothing locked,
-- the pre-migration behaviour for every existing row.
ALTER TABLE books ADD COLUMN locked_fields TEXT NOT NULL DEFAULT '[]';
ALTER TABLE authors ADD COLUMN locked_fields TEXT NOT NULL DEFAULT '[]';
