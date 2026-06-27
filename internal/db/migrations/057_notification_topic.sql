-- +migrate Up
-- ntfy renders a JSON body natively only when the request is POSTed to the
-- server root with a "topic" field. When this column is set, the notifier posts
-- to the root URL instead of the topic URL so ntfy formats the message instead
-- of printing the raw JSON (see issue 1323).
ALTER TABLE notifications ADD COLUMN topic TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE notifications DROP COLUMN topic;
