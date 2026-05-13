-- +migrate Up
-- Per-download-client path remapping. This is intentionally separate from
-- abs.path_remap because ABS and download clients may report different mount
-- prefixes for the same storage.
ALTER TABLE download_clients ADD COLUMN path_remap TEXT NOT NULL DEFAULT '';
