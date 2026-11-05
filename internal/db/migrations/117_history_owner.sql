-- +migrate Up

-- Give every history row an owner of its own.
--
-- Until now a history row was scoped through its book: a user saw the rows
-- whose book they owned. history.book_id is ON DELETE SET NULL, so deleting a
-- book left its rows with no book and therefore no owner, and the user scoped
-- list showed bookless rows to everyone. The owner now lives on the row, so a
-- book's history stays with whoever owned it after the book is gone.
--
-- NULL means no known owner. Under tenancy a NULL owned row is shown to a non
-- admin only while its book exists and is visible to them; a NULL owned row
-- with no book is shown to admins only.
ALTER TABLE history ADD COLUMN owner_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL;

-- Both steps copy an owner only when that user still exists. An owner id
-- left behind by a user deleted while foreign keys were off would otherwise
-- fail the users(id) foreign key, roll the migration back and keep Bindery
-- from starting. Such a row stays unowned (the guard migration 076 uses).

-- 1. The book's owner, for every row that still has its book.
UPDATE history
SET owner_user_id = (
    SELECT b.owner_user_id FROM books b
    WHERE b.id = history.book_id
      AND b.owner_user_id IN (SELECT id FROM users)
)
WHERE book_id IS NOT NULL;

-- 2. The download's owner, for rows the book could not attribute (the book
-- was deleted, never linked, or is shared). Grab, failure, stall and import
-- rows record the release GUID in data, and downloads.guid is unique, so the
-- match names at most one download. CASE keeps json_extract away from a data
-- value that is not valid JSON.
UPDATE history
SET owner_user_id = (
    SELECT d.owner_user_id FROM downloads d
    WHERE d.guid = CASE WHEN json_valid(history.data)
                        THEN json_extract(history.data, '$.guid') END
      AND d.owner_user_id IN (SELECT id FROM users)
)
WHERE owner_user_id IS NULL
  AND CASE WHEN json_valid(data) THEN json_type(data, '$.guid') END = 'text';

CREATE INDEX idx_history_owner_user_id ON history(owner_user_id);

-- Stored recommendations of users who are not admins were built from every
-- user's library and could name other users' books. The engine now limits a
-- user's run to their own and shared books under tenancy, but a stored batch
-- would keep showing until that user refreshes, and the nightly job runs only
-- for the operator. Clear them so the next refresh rebuilds them. Dismissals
-- and author exclusions live in their own tables and are kept, so a rebuilt
-- batch still honours them. Admin batches are unchanged by the fix and kept.
DELETE FROM recommendations
WHERE user_id IN (SELECT id FROM users WHERE role <> 'admin');

-- +migrate Down

DROP INDEX IF EXISTS idx_history_owner_user_id;
ALTER TABLE history DROP COLUMN owner_user_id;
