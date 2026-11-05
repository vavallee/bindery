-- +migrate Up
-- Series monitoring (#2523).
--
-- series_monitoring holds what the scheduled series check needs beyond the
-- series.monitored flag: which books to add (mode), whose series it is
-- (owner_user_id, 0 when unknown or tenancy is off), when it was last checked
-- and how that went, and when it is next due. It is a side table so no
-- existing series query changes. A monitored series with no row here has never
-- been checked and is due at once. next_check_at is the cursor the job walks,
-- separate from last_check_at so a provider failure can be retried sooner than
-- the interval without lying about when the last check happened. last_ok_at is
-- when a check last read the whole catalogue, and is what Upcoming mode compares
-- release dates against, so a book released while the provider was down is
-- still picked up by the next good check.
-- NOTE: no semicolons inside comments, the migration runner splits on them.
CREATE TABLE IF NOT EXISTS series_monitoring (
    series_id        INTEGER PRIMARY KEY REFERENCES series(id) ON DELETE CASCADE,
    mode             TEXT     NOT NULL DEFAULT 'upcoming',
    owner_user_id    INTEGER  NOT NULL DEFAULT 0,
    last_check_at    DATETIME,
    last_ok_at       DATETIME,
    next_check_at    DATETIME,
    last_status      TEXT     NOT NULL DEFAULT '',
    last_message     TEXT     NOT NULL DEFAULT '',
    last_created     INTEGER  NOT NULL DEFAULT 0
);

-- A book with a row here is Upcoming: it is held out of the scheduled wanted
-- search until until_at passes. Written only by the series check, which keeps
-- until_at in step with the provider's release date, and never by hand.
CREATE TABLE IF NOT EXISTS book_search_holds (
    book_id   INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
    until_at  DATETIME NOT NULL
);
