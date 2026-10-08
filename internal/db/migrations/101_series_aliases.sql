-- +migrate Up
-- A series merge (#2554) deletes the merged-away series and keeps each one's
-- provider foreign id here, pointing at the series it was merged into. A
-- refresh never removes a membership, so a provider that still reports the old
-- id would otherwise recreate the deleted series and refile its books there.
-- SeriesRepo resolves every provider id through this table first.
--
-- An alias never equals a live series.foreign_id. Deleting the target drops
-- its aliases, so a provider that still reports them creates the series anew:
-- the user deleted it.
CREATE TABLE series_aliases (
    foreign_id TEXT     PRIMARY KEY,
    series_id  INTEGER  NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_series_aliases_series ON series_aliases(series_id);

-- +migrate Down

DROP TABLE IF EXISTS series_aliases;
