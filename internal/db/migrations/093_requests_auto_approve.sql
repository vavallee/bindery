-- +migrate Up

-- Per-account auto approval for requests (#2718). When set, a request from
-- this account runs the same claim-then-add path an admin's Approve runs,
-- immediately, instead of waiting in the queue.
--
-- Off by default, so every existing install keeps approving by hand. The flag
-- lives on the user row rather than in the settings table because it is a
-- per-account permission, like role, and the admin user API already owns that
-- surface.
ALTER TABLE users ADD COLUMN requests_auto_approve INTEGER NOT NULL DEFAULT 0;

-- +migrate Down

ALTER TABLE users DROP COLUMN requests_auto_approve;
