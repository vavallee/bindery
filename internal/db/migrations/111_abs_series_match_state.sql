-- +migrate Up
-- Remembers what the ABS import's Hardcover series match concluded for an item
-- so a steady state run stops re-querying Hardcover for it (#2173).
--
-- outcome is 'matched' (a series link was made, never retried while input_hash
-- is unchanged) or 'miss' (searched, nothing usable, retried once checked_at is
-- older than the TTL). input_hash digests everything that feeds the match, so a
-- changed item re-matches. One row per ABS item, rewritten on every real match.
-- NOTE: the migration runner splits on semicolons, so keep them out of comments.
CREATE TABLE IF NOT EXISTS abs_series_match_state (
    source_id  TEXT NOT NULL,
    library_id TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    outcome    TEXT NOT NULL,
    checked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source_id, library_id, item_id)
);

-- +migrate Down
-- Additive and only read by the ABS import, so leaving it in place is harmless.
