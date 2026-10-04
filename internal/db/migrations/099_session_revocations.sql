-- +migrate Up
-- session_revocations is the logout denylist. Session cookies are signed and
-- self contained, so before this table logout only cleared the browser's copy
-- and a copied cookie stayed valid until it expired, up to 30 days. Logout now
-- records the token here and the auth middleware rejects it.
--
-- token_hash is the hex SHA-256 of the whole cookie value
-- (auth.SessionTokenHash), never the token itself, so reading this table does
-- not hand out sessions. expires_at is the token's own expiry in unix
-- seconds: after that the signature check rejects the cookie anyway, so the
-- row has no further use and is purged. The table therefore only ever holds
-- tokens signed out within the last 30 days.
CREATE TABLE IF NOT EXISTS session_revocations (
    token_hash TEXT    PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

-- +migrate Down

DROP TABLE IF EXISTS session_revocations;
