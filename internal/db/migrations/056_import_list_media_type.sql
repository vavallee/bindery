-- +migrate Up
-- Per-list media type for import lists. A Hardcover list synced today takes the
-- book's media type straight from Hardcover's edition availability, so a user
-- with separate "Audiobooks" and "Ebooks" Hardcover lists gets the same media
-- type on both (most popular works have both editions). This column lets a list
-- pin the format its synced books are created as.
--
-- Empty string means unset and keeps the historical behaviour (use the
-- Hardcover-derived media type). Non-empty values are validated by the API to
-- one of ebook, audiobook, or both. The override applies only when a list
-- creates a book. The syncer skips books that already exist, so a book on two
-- single-format lists is never auto-promoted to both, and a manually-set media
-- type is never clobbered on re-sync.
ALTER TABLE import_lists ADD COLUMN media_type TEXT NOT NULL DEFAULT '';

-- +migrate Down
-- SQLite < 3.35 cannot DROP COLUMN. The forward migration is additive and the
-- column is ignored by older code, so the down migration is a no-op.
