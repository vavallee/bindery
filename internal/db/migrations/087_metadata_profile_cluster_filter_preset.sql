-- +migrate Up

-- #2235 Phase 2: ClusterEditionCountSignal, gated behind a named preset
-- rather than the raw keep_threshold/exclude_threshold columns migration 086
-- added. Those two columns stay locked to 0/0 by
-- internal/api/metadata_profiles.go's validateScoreThresholds — this column
-- is deliberately the only way a profile can move off that v1 parity
-- default, and only to one of a closed set of server-owned tunings
-- (internal/metadata/filterengine.ClusterFilterPreset), never to an
-- arbitrary threshold pair a client could pick blind.
--
-- Every existing profile gets 'off': ClusterEditionCountSignal is never
-- constructed, exclude_threshold is left at 0, and sync behavior is
-- byte-identical to before this column existed. See
-- TestFetchAuthorBooks_ClusterFilterOffMatchesPreExistingBehavior.
ALTER TABLE metadata_profiles ADD COLUMN cluster_filter_preset TEXT NOT NULL DEFAULT 'off';

-- +migrate Down
-- Non-reversible: SQLite's ALTER TABLE cannot drop a column on the SQLite
-- versions this project supports without a full table rebuild, which none of
-- the other additive migrations in this directory do for a plain column add.
