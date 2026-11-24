-- +migrate Up

-- Point authors whose metadata profile no longer exists at one that does
-- (#4132). authors.metadata_profile_id has no foreign key, and the Add Book and
-- Add Author paths stamped a hard coded id 1, so an install that had deleted
-- the seeded profile 1 kept creating authors that named nothing. Search,
-- import, catalogue sync and reconcile each read that as something different.
--
-- The target is the profile named by author.default_metadata_profile_id when
-- that profile exists, otherwise the lowest id still present. Nothing changes
-- when no profile exists at all, and a second run finds no orphan left, so the
-- statement is idempotent. NULL ids are left alone: the resolver already reads
-- them as "use the default".
UPDATE authors
SET metadata_profile_id = COALESCE(
        (SELECT mp.id FROM settings s
           JOIN metadata_profiles mp ON mp.id = CAST(s.value AS INTEGER)
          WHERE s.key = 'author.default_metadata_profile_id'),
        (SELECT MIN(id) FROM metadata_profiles))
WHERE metadata_profile_id IS NOT NULL
  AND metadata_profile_id NOT IN (SELECT id FROM metadata_profiles)
  AND EXISTS (SELECT 1 FROM metadata_profiles);

-- +migrate Down

-- The old ids named profiles that do not exist, so there is nothing to restore.
SELECT 1;
