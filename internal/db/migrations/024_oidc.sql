-- +migrate Up

-- OIDC identity columns on users. All nullable so local-password users are
-- unaffected. The composite UNIQUE index on (oidc_issuer, oidc_sub) prevents
-- sub re-use across providers (two IdPs can emit sub=1234 for different humans).
ALTER TABLE users ADD COLUMN oidc_sub      TEXT;
ALTER TABLE users ADD COLUMN oidc_issuer   TEXT;
ALTER TABLE users ADD COLUMN email         TEXT;
ALTER TABLE users ADD COLUMN display_name  TEXT;

CREATE UNIQUE INDEX idx_users_oidc ON users (oidc_issuer, oidc_sub)
    WHERE oidc_issuer IS NOT NULL AND oidc_sub IS NOT NULL;

-- +migrate Down

DROP INDEX IF EXISTS idx_users_oidc;
