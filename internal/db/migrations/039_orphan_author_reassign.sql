-- Reassign authors whose owner_user_id points to a deleted user to NULL so
-- they become visible to all users. This fixes installs where the admin user
-- was deleted and recreated (gaining a new ID), leaving pre-existing authors
-- permanently invisible despite blocking re-creation via the duplicate check.
UPDATE authors
SET owner_user_id = NULL
WHERE owner_user_id IS NOT NULL
  AND owner_user_id NOT IN (SELECT id FROM users);
