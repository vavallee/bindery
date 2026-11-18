-- +migrate Up

-- Options of the Audible Sync wishlist import list (internal/audiblesynclistsyncer).
--
-- skip_owned: leave a wishlist book alone when the Audible account already owns
-- it. Audible Sync downloads owned books itself, so searching an indexer for one
-- would fetch a second copy. On by default: it is what an operator running
-- Audible Sync expects.
--
-- remove_on_import: once Bindery has imported a wishlist book, take it off the
-- Audible wishlist. Off by default, because it writes to the operator's Audible
-- account.
--
-- Both are ignored by every other list type.
ALTER TABLE import_lists ADD COLUMN skip_owned INTEGER NOT NULL DEFAULT 1;
ALTER TABLE import_lists ADD COLUMN remove_on_import INTEGER NOT NULL DEFAULT 0;

-- +migrate Down
ALTER TABLE import_lists DROP COLUMN remove_on_import;
ALTER TABLE import_lists DROP COLUMN skip_owned;
