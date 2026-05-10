-- +migrate Up

-- Remove the hardcoded 'move' default inserted by migration 013.
-- Bindery now defaults to 'hardlink' on same-filesystem imports and
-- 'copy' on cross-filesystem imports. Both modes preserve seeding.
-- Users who explicitly set import.mode to copy/hardlink/external are
-- not affected. Users who want 'move' can restore it via Settings.
DELETE FROM settings WHERE key = 'import.mode' AND value = 'move';

-- +migrate Down
INSERT OR IGNORE INTO settings (key, value) VALUES ('import.mode', 'move');
