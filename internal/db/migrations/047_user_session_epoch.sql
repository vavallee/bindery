-- +migrate Up

-- Per-user session epoch. Bumped on password change (self-serve or admin
-- reset) to invalidate every outstanding session cookie for that user, the
-- "logout everywhere" primitive (Wave 1 / Bundle C audit finding).
--
-- The cookie payload carries the epoch under which it was minted. The auth
-- middleware loads users.session_epoch on every request and rejects the
-- cookie when the two disagree. Bumping the column therefore evicts
-- stolen-cookie attackers the instant the rightful owner rotates their
-- password.
--
-- Default = 1 (not 0) so pre-migration cookies, which carry no epoch field
-- and decode as epoch=0, fail the comparison on upgrade. That is the
-- deliberate forced-logout-on-upgrade behaviour called out as a breaking
-- change in the release notes. On a 100k-user table this default-fill is a
-- single cheap UPDATE with no constraint check, safe to run online.
ALTER TABLE users ADD COLUMN session_epoch INTEGER NOT NULL DEFAULT 1;

-- +migrate Down

-- SQLite does not support DROP COLUMN in older versions, so rely on a
-- schema rebuild if a downgrade is ever needed. Best-effort for dev
-- rollbacks only, mirroring the convention used by 025_multiuser.sql.
