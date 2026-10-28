-- +migrate Up

-- Who made a request when it was not its owner (#3337). An admin session or
-- the API key may create a request on behalf of another account with the
-- forUser field on POST /requests; the row is owned by that account exactly as
-- if they had asked, and these two columns record who actually did.
--
--   created_via         '' the owner asked themselves (every existing row),
--                       'api_key' the API key asked for them,
--                       'admin' an admin session asked for them.
--   created_by_user_id  the admin who asked, for 'admin'. NULL otherwise: the
--                       API key is not a person, and recording the first admin
--                       account it resolves to would name someone who did not
--                       make the request.
--
-- This lives on the request row rather than in the history table: history
-- rows with no book are shown to every user, and this is admin queue
-- information.
ALTER TABLE requests ADD COLUMN created_via TEXT NOT NULL DEFAULT ''
    CHECK (created_via IN ('', 'api_key', 'admin'));
ALTER TABLE requests ADD COLUMN created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL;

-- +migrate Down

ALTER TABLE requests DROP COLUMN created_by_user_id;
ALTER TABLE requests DROP COLUMN created_via;
