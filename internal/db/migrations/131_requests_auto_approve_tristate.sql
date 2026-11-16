-- +migrate Up

-- Per-account request auto approval becomes tri-state (#3900): NULL means the
-- account has no setting of its own and follows requests.auto_approve_default,
-- 1 is Always, 0 is Never.
--
-- Migration 093 made requests_auto_approve INTEGER NOT NULL DEFAULT 0. SQLite
-- cannot drop a NOT NULL in place, and rebuilding the users table would mean
-- rebuilding around every foreign key that points at it, so the tri-state
-- lives in a new nullable column and the old one is left behind, unread.
--
-- Every existing row is copied across as an explicit 0 or 1. An admin's
-- deliberate "off" cannot be told apart from "never touched", so each existing
-- account keeps behaving exactly as it does today and turning the new default
-- on later never enables an account that was at 0. Only accounts created after
-- this migration are NULL.
ALTER TABLE users ADD COLUMN requests_auto_approve_override INTEGER;

UPDATE users SET requests_auto_approve_override = requests_auto_approve;

-- +migrate Down

-- Fold the tri-state back into the old column: Always stays 1, Never and
-- unset become 0 (the default did not exist before this migration).
UPDATE users SET requests_auto_approve = COALESCE(requests_auto_approve_override, 0);

ALTER TABLE users DROP COLUMN requests_auto_approve_override;
