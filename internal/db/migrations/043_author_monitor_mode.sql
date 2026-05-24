-- +migrate Up
-- Per-author monitor defaults control which newly discovered books start as
-- monitored. Existing authors keep the historical "all books" behaviour.
ALTER TABLE authors ADD COLUMN monitor_mode TEXT NOT NULL DEFAULT 'all';
ALTER TABLE authors ADD COLUMN monitor_latest_count INTEGER NOT NULL DEFAULT 1;
