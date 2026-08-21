-- +migrate Up
-- Finish the orphan-owner repair that migration 039 started (#1899).
--
-- Before #1727 turned foreign key enforcement on per connection, deleting a
-- user succeeded and silently orphaned every row that pointed at them. 039
-- swept exactly one table, authors, because that was the symptom being chased
-- at the time: authors stranded under a deleted-and-recreated admin were
-- invisible to everyone while still blocking re-creation via the duplicate
-- check.
--
-- The same orphaning happened to the other six owner tables, and to the
-- blocklist audit column added later. On those installs the rows are owned by
-- a user id that no longer exists, which no per-user query can match, so the
-- data is unreachable without being gone.
--
-- NULL is this schema's "shared with every user", which is the same answer 039
-- chose and the only one available here: which surviving account should
-- inherit an orphan is not recorded anywhere, and guessing would hand one
-- user's library to another. Going forward this state is not reachable, because
-- UserRepo.Delete now makes the admin choose before the user row goes away.
UPDATE books
SET owner_user_id = NULL
WHERE owner_user_id IS NOT NULL
  AND owner_user_id NOT IN (SELECT id FROM users);

UPDATE quality_profiles
SET owner_user_id = NULL
WHERE owner_user_id IS NOT NULL
  AND owner_user_id NOT IN (SELECT id FROM users);

UPDATE metadata_profiles
SET owner_user_id = NULL
WHERE owner_user_id IS NOT NULL
  AND owner_user_id NOT IN (SELECT id FROM users);

UPDATE downloads
SET owner_user_id = NULL
WHERE owner_user_id IS NOT NULL
  AND owner_user_id NOT IN (SELECT id FROM users);

UPDATE root_folders
SET owner_user_id = NULL
WHERE owner_user_id IS NOT NULL
  AND owner_user_id NOT IN (SELECT id FROM users);

UPDATE import_lists
SET owner_user_id = NULL
WHERE owner_user_id IS NOT NULL
  AND owner_user_id NOT IN (SELECT id FROM users);

-- Blocklist attribution is an audit trail: migration 050 defines NULL as
-- "unknown origin", which is what an entry promoted by a since-deleted user
-- honestly is. The entry itself is global and stays.
UPDATE blocklist
SET created_by_user_id = NULL
WHERE created_by_user_id IS NOT NULL
  AND created_by_user_id NOT IN (SELECT id FROM users);

-- Re-run 039's sweep. It shipped without a +migrate Up marker, and an install
-- that gained orphaned authors after 039 ran would still be carrying them.
UPDATE authors
SET owner_user_id = NULL
WHERE owner_user_id IS NOT NULL
  AND owner_user_id NOT IN (SELECT id FROM users);

-- +migrate Down
-- Irreversible: which user each orphaned row belonged to is not recorded.
