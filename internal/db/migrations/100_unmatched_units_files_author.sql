-- +migrate Up
-- files_author is the author a library adoption unit's files name (their
-- Artist, Album Artist or Composer tags, or a track naming pattern) when that
-- is not the unit's author folder, and '' when they agree or say nothing
-- (#2942). The scan decides it with every author tag in hand; the adoption
-- page only reads it. Deriving the conflict from parsed_author against
-- author_folder instead flagged a correctly placed book whose Artist tag holds
-- the narrator. Existing rows start at '' and show no conflict until the next
-- scan rewrites them.
ALTER TABLE unmatched_units ADD COLUMN files_author TEXT NOT NULL DEFAULT '';

-- +migrate Down

ALTER TABLE unmatched_units DROP COLUMN files_author;
