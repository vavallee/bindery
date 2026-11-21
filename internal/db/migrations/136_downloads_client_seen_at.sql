-- +migrate Up

-- When a poll first found this download's source in its download client
-- (#4111). The importer fails a download the client has no record of ten
-- minutes after the grab, and that failure means two different things: the
-- client never received it (counted towards a blocklist, under the rules in
-- internal/releasefail), or the client had it and it was removed (a person
-- cancelled it, the client dropped it, it was wiped), which says nothing about
-- the release and is never counted. This column tells them apart. It is
-- written once, on the first sighting, and cleared when the row is re-grabbed.
ALTER TABLE downloads ADD COLUMN client_seen_at DATETIME;

-- A row that is live when this runs predates the evidence: nothing recorded
-- whether its client ever listed it, so it must not later read as "never
-- arrived". Marking it seen makes an upgrade unable to make old rows count.
UPDATE downloads SET client_seen_at = CURRENT_TIMESTAMP WHERE status IN ('grabbed', 'downloading');

-- +migrate Down

ALTER TABLE downloads DROP COLUMN client_seen_at;
