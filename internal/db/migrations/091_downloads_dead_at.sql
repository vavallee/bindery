-- +migrate Up
-- Record the moment a download DIES, so the scheduler's automatic re-grab
-- cooldown can measure from it (#2710).
--
-- The table had no such stamp. added_at is when the row was written, grabbed_at
-- when it started downloading, completed_at when the files finished; none of
-- them move when the download later fails, and SetError writes only status and
-- error_message. A cooldown measured from those columns counts from the wrong
-- instant: a torrent grabbed at noon that the client errors at ten in the
-- evening is already past a six hour window at the moment it dies, so the very
-- next sweep would re-grab it, which is the loop the cooldown exists to stop.
--
-- NULL = the row is not dead, or died before this column existed. It is cleared
-- again whenever a row is claimed for a re-grab, so it always describes the
-- attempt the row currently holds.
ALTER TABLE downloads ADD COLUMN dead_at DATETIME;

-- Backfill the rows that are already dead. There is no record of when they
-- died, so this uses the best evidence the row carries, which is the same
-- approximation the cooldown used before this column existed. It is only ever
-- too early, never too late: every one of these rows is at least as old as the
-- stamp, so nothing is held back longer than it should be, and the months old
-- failures from the report stay immediately eligible.
UPDATE downloads
SET dead_at = COALESCE(completed_at, grabbed_at, added_at)
WHERE dead_at IS NULL AND status IN ('failed', 'importBlocked');
