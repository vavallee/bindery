-- +migrate Up

-- Narrator list and edition type on the edition row (#1358).
--
-- narrators is a JSON array of names, edition_type one of unabridged,
-- abridged, dramatized, full_cast, or empty for unknown. Both default to
-- "nothing known", so existing rows read exactly as before and
-- books.narrator keeps being the display value. Readers fall back to the
-- book's narrator string when the list is empty; no data is rewritten here.
ALTER TABLE editions ADD COLUMN narrators TEXT NOT NULL DEFAULT '[]';
ALTER TABLE editions ADD COLUMN edition_type TEXT NOT NULL DEFAULT '';

-- Per profile preferences. Empty means off, which is today's ranking.
ALTER TABLE quality_profiles ADD COLUMN preferred_narrators TEXT NOT NULL DEFAULT '[]';
ALTER TABLE quality_profiles ADD COLUMN edition_preference TEXT NOT NULL DEFAULT '';

-- +migrate Down

ALTER TABLE quality_profiles DROP COLUMN edition_preference;
ALTER TABLE quality_profiles DROP COLUMN preferred_narrators;
ALTER TABLE editions DROP COLUMN edition_type;
ALTER TABLE editions DROP COLUMN narrators;
