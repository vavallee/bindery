-- +migrate Up
-- Track the provenance of an indexer's seed_ratio so the Prowlarr syncer can
-- auto-populate the value (#1065) without ever clobbering a choice the user made
-- explicitly. Values:
--   ''         unset / never touched, so Prowlarr may fill it.
--   'prowlarr' auto-populated from Prowlarr's seedCriteria.seedRatio, and a
--              later Prowlarr change may refresh it.
--   'user'     the user set, cleared, or toggled it via the UI, so Prowlarr must
--              not touch seed_ratio again (an explicit clear-to-null sticks).
-- Existing rows predate auto-population. A row with a non-NULL seed_ratio was
-- necessarily set by the user (Prowlarr did not write it before this feature),
-- so it is backfilled to 'user' to protect those overrides. NULL rows stay
-- unset and remain eligible for Prowlarr auto-population.
ALTER TABLE indexers ADD COLUMN seed_ratio_source TEXT NOT NULL DEFAULT '';

UPDATE indexers SET seed_ratio_source = 'user' WHERE seed_ratio IS NOT NULL;

-- +migrate Down

ALTER TABLE indexers DROP COLUMN seed_ratio_source;
