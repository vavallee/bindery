-- +migrate Up
-- D4b (deep audit follow-up). Adds an audit column recording which user
-- promoted a row into the blocklist. Audit only. The decision spec
-- (internal/decision/specs.go BlocklistedSpec) matches on GUID alone, so
-- blocklist semantics stay global on purpose. A failed release (broken
-- .nzb, virus, stalled grab) is broken for every user, and per-user
-- blocklist would force user 2 to re-pay the cost of user 1's failed grab.
--
-- IsBlocked, List, BlocklistedSpec, DeleteByID and DeleteByBookID are NOT
-- changed by this migration. The column is read by an admin view that
-- displays "blocklisted by X" without altering query semantics.
--
-- No backfill. Existing rows predate the audit column and have no honest
-- owner. Readarr-imported rows have book_id IS NULL and were never tied
-- to a real user, and backfilling them to user 1 would lie about
-- provenance. NULL means "unknown origin", which is the correct answer
-- for legacy rows and for system-write paths (scheduler stall-detection,
-- readarr import migration) that intentionally leave the field empty.
ALTER TABLE blocklist ADD COLUMN created_by_user_id INTEGER REFERENCES users(id);
CREATE INDEX IF NOT EXISTS idx_blocklist_created_by ON blocklist(created_by_user_id);

-- +migrate Down

DROP INDEX IF EXISTS idx_blocklist_created_by;
-- SQLite < 3.35 cannot DROP COLUMN. The forward migration is additive and
-- defaults to NULL on existing rows so a rollback that leaves the column
-- in place is a no-op for callers. If a true rollback is required, the
-- operator must rebuild the table manually.
