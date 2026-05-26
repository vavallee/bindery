-- +migrate Up
-- Per-author monitored-series join (#810). Holds the subset of series the
-- author is pinned to when authors.monitor_mode = 'series'. Kept separate
-- from series.monitored (a global watchlist flag) so the per-author selection
-- never silently flips series-wide state for other authors that share a
-- series. ON DELETE CASCADE on both sides drops the row when either parent
-- vanishes — there is no business reason to retain a dangling pin.
CREATE TABLE author_monitored_series (
    author_id  INTEGER NOT NULL,
    series_id  INTEGER NOT NULL,
    created_at DATETIME NOT NULL,
    PRIMARY KEY (author_id, series_id),
    FOREIGN KEY (author_id) REFERENCES authors(id) ON DELETE CASCADE,
    FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE
);

CREATE INDEX idx_author_monitored_series_author ON author_monitored_series(author_id);
CREATE INDEX idx_author_monitored_series_series ON author_monitored_series(series_id);
