-- +migrate Up

-- Ebook upgrade sweep (#2420, #1357).
--
-- quality_profiles.upgrade_allowed has defaulted to 1 since migration 001 and
-- nothing read it until the upgrade sweep. A profile an operator edited after
-- #2373 round-tripped whatever it held, so many rows say "upgrades allowed"
-- without anyone having chosen it. Left alone, the sweep would start
-- re-searching their imported ebooks on the first run after this upgrade.
-- Clear the flag everywhere once: upgrading is now something a profile opts
-- into from Settings, Quality, and the default is off.
UPDATE quality_profiles SET upgrade_allowed = 0;

-- Whether a verified upgrade deletes the file it replaced. 0 keeps the old
-- file next to the new one, which is the safe default: nothing is removed
-- unless the profile says so.
ALTER TABLE quality_profiles ADD COLUMN remove_replaced INTEGER NOT NULL DEFAULT 0;

-- +migrate Down
ALTER TABLE quality_profiles DROP COLUMN remove_replaced;
