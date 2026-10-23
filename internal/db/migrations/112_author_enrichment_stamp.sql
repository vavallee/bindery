-- +migrate Up
-- When the author enrichers last gave a full answer for an author (#3287).
--
-- Enrichment runs only on the staggered profile refresh and the manual
-- refresh, and an answer, a miss included, holds for 30 days. The enricher's
-- own cache is in memory and empties on every restart, so without a stamp a
-- restart would send every author to Wikidata again on the next pass. NULL
-- means never enriched, or the last attempt failed and should be retried.
-- Written by a single column UPDATE, like last_discovery_at, so a user edit
-- racing a refresh loses nothing.
-- NOTE: no semicolons inside comments, the migration runner splits on them.
ALTER TABLE authors ADD COLUMN last_enrichment_at DATETIME;

-- +migrate Down
-- SQLite before 3.35 cannot DROP COLUMN. The column is additive and NULL
-- reads as never enriched, so leaving it is a no-op.
