-- +migrate Up
-- ensureAuthor (internal/hardcoverlistsyncer/syncer.go) built list-sync
-- authors by hand and never stamped a metadata profile, unlike every other
-- author-create path (applyAuthorCreateOptions), which has always defaulted
-- to the seeded "Standard" profile (id=1) when the caller sent none (#1736).
--
-- The code path is now fixed for new authors; this backfills existing rows
-- so already-affected libraries converge without hand-editing, matching the
-- exact default applyAuthorCreateOptions would have stamped at create time.
UPDATE authors
SET metadata_profile_id = 1
WHERE metadata_profile_id IS NULL
  AND EXISTS (SELECT 1 FROM metadata_profiles WHERE id = 1);

-- +migrate Down
-- Irreversible: the rows this backfilled were already NULL before the fix,
-- and there is no record of which rows were touched versus already 1.
