-- +migrate Up
-- Persist per-indexer search health (#1935).
--
-- An indexer answering every search with "indexer error 101: Account
-- suspended" looked completely normal in Settings: enabled, no badge, no
-- warning, nothing notified. The only place the failure appeared was the
-- interactive search details panel, and only if the user happened to run one
-- and expand it. An expired subscription or a revoked API key could sit there
-- for weeks quietly removing an indexer from every automatic grab decision.
--
-- Nothing stored on the row said anything about whether the indexer works. The
-- only relevant column was `enabled`, which only the user ever writes, and the
-- rate-limit cooldown added by #1934 lives in memory on the shared Searcher and
-- is lost on restart.
--
-- last_error_code carries the Newznab code so the UI and the notifier can tell
-- the two failure kinds apart: 1xx (bad credentials, suspended account, VPN
-- forbidden) needs a human, while a 5xx clears on its own. NULL means the
-- failure was not a Newznab rejection at all, e.g. a connection error.
ALTER TABLE indexers ADD COLUMN last_error TEXT;
ALTER TABLE indexers ADD COLUMN last_error_code INTEGER;
ALTER TABLE indexers ADD COLUMN last_failure_at DATETIME;
ALTER TABLE indexers ADD COLUMN last_success_at DATETIME;

-- +migrate Down
-- SQLite before 3.35 cannot DROP COLUMN. The forward migration is additive and
-- these columns default to NULL, which every reader treats as "never checked",
-- so leaving them in place is a no-op for callers.
