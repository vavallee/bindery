-- Per-author policy for works discovered AFTER the initial catalogue sync
-- (issue #1348). 'all' follows the author's monitor_mode (previous behaviour)
-- while 'none' creates newly-discovered works unmonitored so a metadata
-- refresh can never mass-monitor a back-catalogue. Existing rows keep 'all'
-- to preserve behaviour. Importers create authors with 'none'.
-- NOTE: no semicolons inside comments -- the migration runner splits on them.
ALTER TABLE authors ADD COLUMN monitor_new_items TEXT NOT NULL DEFAULT 'all';
