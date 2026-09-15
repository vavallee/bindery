-- +migrate Up

-- Scored-signal filtering (#2235). internal/metadata/filterengine replaces
-- the sequential boolean noise-filtering in the author catalogue sync with
-- signals that emit weighted observations, summed into a score and banded
-- into KEEP / REVIEW / EXCLUDE.
--
-- Every existing profile gets 0/0. With every v1 signal at veto weight
-- (-1000, see filterengine.vetoWeight) and a Prior of 0, a clean record
-- scores 0 and lands KEEP, and any single firing signal scores -1000 and
-- lands EXCLUDE. That reproduces the pre-#2235 boolean chain's keep/exclude
-- decision for every profile exactly — see
-- filterengine.TestRegistryIsVetoOnlyAtV1, which fails the build if a future
-- signal is registered at any other weight without this default being
-- revisited.
--
-- keep_threshold = exclude_threshold = 0 is also the only pair the API layer
-- (internal/api/metadata_profiles.go) accepts at v1 — not merely an equal
-- pair, but exactly zero: a nonzero equal pair (e.g. both 50) still bands
-- every clean candidate as EXCLUDE, since a clean candidate's score is
-- always exactly 0 under the current veto-only, Prior=0 design. With no
-- signal graded enough to populate a REVIEW band meaningfully either, any
-- other value is rejected outright rather than silently accepted and either
-- unreachable or actively wrong.
--
-- REAL, not INTEGER: a future graded signal's units are not integral
-- (Weight * Confidence, e.g. -260 * 0.35 = -91).
ALTER TABLE metadata_profiles ADD COLUMN keep_threshold REAL NOT NULL DEFAULT 0;
ALTER TABLE metadata_profiles ADD COLUMN exclude_threshold REAL NOT NULL DEFAULT 0;

-- +migrate Down
-- Non-reversible: SQLite's ALTER TABLE cannot drop a column on the SQLite
-- versions this project supports without a full table rebuild, which none of
-- the other additive migrations in this directory do for a plain column add.
