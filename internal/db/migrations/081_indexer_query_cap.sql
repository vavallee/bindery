-- +migrate Up
-- Per-indexer daily query cap (#2312).
--
-- One scheduled wanted sweep searches every wanted book against every enabled
-- indexer back to back, and nothing stopped at any threshold. A large library on
-- several private trackers could therefore spend a whole day's API allowance in a
-- single burst, locking the account out for everything else pointed at that
-- tracker. Lengthening search.interval does not help: it changes how often the
-- burst happens, not how big one burst is, and 168h is already the maximum.
--
-- daily_query_limit is the user's cap, in outbound HTTP requests rather than
-- books, because one book on one indexer costs between 1 and 8 requests
-- depending on how far the tier cascade falls through. NULL (and any value <= 0)
-- means no cap, which is what every existing row gets on upgrade.
--
-- indexer_query_counts holds one row per indexer per hour. Usage is the sum of
-- the buckets inside the window, so the budget frees up gradually as buckets age
-- out instead of the whole allowance unlocking at one instant. The boundary is
-- rounded down to the hour, which means a partially elapsed bucket is counted in
-- full and the window can span 25 buckets rather than 24. That errs towards
-- counting too much, never too little, which is the right direction for a cap
-- whose whole job is not to exceed someone else's allowance. Rows older than the
-- window are pruned on write, so an indexer never accumulates more than that.
--
-- Counter writes must never touch indexers.updated_at: the rate-limit cooldown
-- (#1934) reads a newer updated_at as "the user edited this row, try again" and
-- clears the cooldown. That is why the counts live in their own table rather
-- than in columns on indexers, and why RecordSearchFailure (#1935) already
-- leaves updated_at alone.
ALTER TABLE indexers ADD COLUMN daily_query_limit INTEGER;

CREATE TABLE indexer_query_counts (
    indexer_id INTEGER NOT NULL REFERENCES indexers(id) ON DELETE CASCADE,
    hour_start DATETIME NOT NULL,
    count      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (indexer_id, hour_start)
);

-- +migrate Down
DROP TABLE IF EXISTS indexer_query_counts;
-- SQLite before 3.35 cannot DROP COLUMN. daily_query_limit is additive and NULL
-- already reads as "no cap" to every reader, so leaving it in place is a no-op.
