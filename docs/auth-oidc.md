# OIDC Authentication

Bindery v0.24.0 adds native OpenID Connect (Authorization Code + PKCE). Users sign in via Google, GitHub, Authelia, Authentik, Keycloak, or any OIDC-compliant provider. Local password login continues to work alongside OIDC.

## How it works

Bindery acts as an OIDC Relying Party. On login, it redirects to the provider, exchanges the code for tokens, validates the ID token, and maps the user by `(issuer, sub)` — not by email or username, which can change. On first login the user is auto-provisioned as a Bindery account.

Sessions are issued using the same HMAC-signed cookie as password login. OIDC sits in front of session issuance — the rest of Bindery is unaware of the auth path.

## Prerequisites

- Bindery is reachable at a stable base URL (needed for the callback URL).
- You have an OIDC application configured at your IdP with the redirect URI: `<BINDERY_OIDC_REDIRECT_BASE_URL>/api/v1/auth/oidc/<provider-id>/callback`.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BINDERY_OIDC_REDIRECT_BASE_URL` | _(see resolution below)_ | Public base URL Bindery is reachable at (e.g. `https://bindery.example.com`). Used as the prefix for OIDC callback URLs. Required for path-prefix deploys; otherwise optional when behind a trusted proxy that sets `X-Forwarded-Proto` and `X-Forwarded-Host`. |
| `BINDERY_TRUSTED_PROXY` | _(unset)_ | Comma-separated CIDRs of reverse proxies whose `X-Forwarded-*` headers Bindery may trust. Required for the auto-derive fallback to kick in. |
| `BINDERY_LOCAL_AUTH_ENABLED` | `true` | Set to `false` to disable password-based login entirely. `POST /auth/login` returns 403 and the admin user-create API is also blocked. Use when all users must authenticate via OIDC. |
| `BINDERY_OIDC_AUTO_PROVISION` | `true` | Set to `false` to require that OIDC users already exist in Bindery's database. First-time OIDC logins from unknown `(issuer, sub)` pairs return 403 instead of creating an account. |
| `BINDERY_OIDC_EMAIL_LINK` | `false` | Set to `true` to link an unknown OIDC identity to an existing Bindery account if the email address matches. Runs before the auto-provision check. Useful for migrating from local accounts to OIDC without losing history. |

## Redirect URL construction

The callback URL registered in your IdP must exactly match what Bindery sends during the authorization redirect. Bindery constructs it as:

```
<base-url>/api/v1/auth/oidc/<provider-id>/callback
```

### Redirect base URL resolution

Bindery picks the `<base-url>` per request, in this order:

1. **`BINDERY_OIDC_REDIRECT_BASE_URL`** — if set, this wins unconditionally. Use this for path-prefix deploys (`https://example.com/bindery`) or any case where forwarded headers don't reflect the public URL the IdP will see.
2. **`X-Forwarded-Proto` + `X-Forwarded-Host`** — if `BINDERY_TRUSTED_PROXY` is configured *and* the immediate peer is in that CIDR list, Bindery uses the forwarded headers. Untrusted peers can't influence the URL — their headers are ignored.
3. **`r.Host`** — direct-access fallback. The scheme is inferred from the TLS connection or `X-Forwarded-Proto`. Suitable for development; not what you want behind a proxy.

For typical reverse-proxy deploys (Traefik, nginx, Caddy, Kubernetes Ingress) where `BINDERY_TRUSTED_PROXY` already needs to be set for proxy-auth or X-Forwarded-For trust, you can leave `BINDERY_OIDC_REDIRECT_BASE_URL` unset and the redirect URL will track the public hostname automatically.

**Path-prefix example** (Bindery mounted at `/bindery/`):

```
BINDERY_OIDC_REDIRECT_BASE_URL=https://example.com/bindery
# → callback: https://example.com/bindery/api/v1/auth/oidc/<id>/callback
```

The auto-derive path doesn't know about path prefixes — when Bindery is mounted under a sub-path, set the env var explicitly.

### Pinning across the flow

The base URL Bindery resolves at `/login` is stored in the (HttpOnly, short-lived) flow cookie and replayed at `/callback`, so the `redirect_uri` in the token exchange always matches the value the IdP saw at the authorize request. If the proxy chain changes between the two hops (extremely unusual), the callback would still use the originally-resolved URL.

For example, with `BINDERY_OIDC_REDIRECT_BASE_URL=https://bindery.example.com` and provider id `google`:

```
https://bindery.example.com/api/v1/auth/oidc/google/callback
```

## Adding a provider

Providers are configured in **Settings → Security → OIDC Providers** (admin only). Each provider has:

| Field | Description |
|-------|-------------|
| `id` | Short identifier used in callback URL path (e.g. `google`, `authelia`). |
| `name` | Display name shown on the login button. |
| `issuer` | OIDC discovery URL base (e.g. `https://accounts.google.com`). |
| `client_id` | From your IdP app registration. |
| `client_secret` | From your IdP app registration. Stored in the `settings` table — treat the database as sensitive. |
| `scopes` | Space-separated scopes (e.g. `openid email profile`). |
| `allowed_groups` | Optional. Comma-separated IdP groups/roles that are allowed to log in. Empty = allow all authenticated users. |

Providers can also be set directly via the `settings` table key `auth.oidc.providers` (JSON array) for scripted deploys.

## Auth policy controls

Three env vars govern who can log in and how accounts are created. All default to backwards-compatible values — existing deployments are unaffected.

### SSO-only mode (disable local password login)

```
BINDERY_LOCAL_AUTH_ENABLED=false
```

With this set, `POST /auth/login` returns `403 Forbidden` and the OIDC providers are the only way in. The admin user-create API is also blocked. The login page hides the username/password form; if no OIDC providers are configured it shows "Contact your administrator for access".

**Before** setting this: ensure at least one OIDC provider is configured and working, and that you can authenticate through it. There is no break-glass override — if you lock yourself out, you must restart with `BINDERY_LOCAL_AUTH_ENABLED=true` to recover.

### Preventing account self-registration via OIDC

```
BINDERY_OIDC_AUTO_PROVISION=false
```

By default, any user who authenticates via a configured OIDC provider gets a Bindery account created for them automatically. Setting this to `false` requires the admin to pre-create accounts (via `POST /api/v1/auth/users`) before users can log in for the first time. First-time logins from an unknown `(issuer, sub)` pair receive `403 access denied: account not provisioned`.

Useful for closed deployments where you control the user list explicitly.

### Linking OIDC identities to existing local accounts

```
BINDERY_OIDC_EMAIL_LINK=true
```

When `BINDERY_OIDC_EMAIL_LINK=true`, Bindery tries to match an unrecognised OIDC login to an existing account by email address before deciding whether to auto-provision or deny. If the email in the ID token matches a local account's email, the OIDC identity is permanently linked to that account (`oidc_issuer` / `oidc_sub` updated) and the user is logged in.

This is a one-time migration path: once linked, the `(issuer, sub)` pair is stored and subsequent logins bypass the email check. If the email doesn't match any account, the normal auto-provision / deny logic continues.

**Security note:** email linking trusts that the IdP has verified the email address. Only enable this with IdPs that verify email — don't use it with IdPs that allow users to self-assign arbitrary email claims.

### Scenario: fully managed SSO deployment

```
BINDERY_LOCAL_AUTH_ENABLED=false
BINDERY_OIDC_AUTO_PROVISION=false
```

Only users pre-created by an admin can log in, exclusively via OIDC. No local password logins, no self-registration. Pair this with `allowed_groups` on the provider to further restrict who can authenticate.

### `client_secret` write-only semantics

`client_secret` is **never returned** by `GET /api/v1/settings/auth/oidc/providers` — it is write-only. The `PUT` endpoint uses secret-preservation semantics so the Settings UI (which only fetches public config) cannot accidentally blank a secret it never had access to:

| `client_secret` in PUT body | Effect |
|-----------------------------|--------|
| Non-empty string | Secret updated to the new value |
| Empty string `""` or field absent | Existing secret preserved unchanged |
| Empty string on a **new** provider (POST) | Rejected with `400 Bad Request` |

To rotate a secret without touching other fields:

```bash
curl -X PUT http://bindery:8787/api/v1/settings/auth/oidc/providers/google \
  -H "X-Api-Key: <admin-key>" \
  -H "Content-Type: application/json" \
  -d '{"id": "google", "client_secret": "<new-secret>"}'
```

To update scopes or groups without disturbing the secret, omit `client_secret` from the body entirely.

## Google

1. Go to [Google Cloud Console → APIs & Services → Credentials → Create OAuth 2.0 Client](https://console.cloud.google.com/apis/credentials).
2. Application type: **Web application**.
3. Authorized redirect URIs: `https://bindery.example.com/api/v1/auth/oidc/google/callback`.
4. Add provider in Bindery:

```json
{
  "id": "google",
  "name": "Google",
  "issuer": "https://accounts.google.com",
  "client_id": "<your-client-id>.apps.googleusercontent.com",
  "client_secret": "<your-client-secret>",
  "scopes": "openid email profile"
}
```

## GitHub (via Dex)

GitHub's OAuth2 implementation does not expose an OIDC discovery endpoint, so Bindery cannot use it directly. The recommended bridge is [Dex](https://dexidp.io/) with its GitHub connector.

**1. Register a GitHub OAuth App** at github.com → Settings → Developer settings → OAuth Apps. Set the callback URL to your Dex instance: `https://dex.example.com/callback`.

**2. Configure Dex:**

```yaml
# dex/config.yaml
issuer: https://dex.example.com

connectors:
  - type: github
    id: github
    name: GitHub
    config:
      clientID: <github-oauth-app-client-id>
      clientSecret: <github-oauth-app-client-secret>
      redirectURI: https://dex.example.com/callback
      orgs:
        - name: your-github-org   # optional: restrict to org members

staticClients:
  - id: bindery
    secret: <dex-client-secret>
    name: Bindery
    redirectURIs:
      - https://bindery.example.com/api/v1/auth/oidc/github/callback
```

**3. Add provider in Bindery:**

```json
{
  "id": "github",
  "name": "GitHub",
  "issuer": "https://dex.example.com",
  "client_id": "bindery",
  "client_secret": "<dex-client-secret>",
  "scopes": "openid email profile"
}
```

The `sub` claim in Dex's tokens is the GitHub user ID (numeric, stable — does not change on username rename).

## Authelia

Authelia exposes a native OIDC provider in v4.34+. Register a client:

```yaml
# authelia/configuration.yml
identity_providers:
  oidc:
    clients:
      - id: bindery
        secret: '$plaintext$<your-secret>'
        authorization_policy: one_factor
        redirect_uris:
          - https://bindery.example.com/api/v1/auth/oidc/authelia/callback
        scopes:
          - openid
          - email
          - profile
          - groups
```

Bindery provider config:

```json
{
  "id": "authelia",
  "name": "Authelia",
  "issuer": "https://auth.example.com",
  "client_id": "bindery",
  "client_secret": "<your-secret>",
  "scopes": "openid email profile groups",
  "allowed_groups": "bindery-users"
}
```

## Authentik

1. In Authentik admin, create a **Provider → OAuth2/OpenID Provider**.
2. Set redirect URI: `https://bindery.example.com/api/v1/auth/oidc/authentik/callback`.
3. Create an **Application** linked to the provider.

```json
{
  "id": "authentik",
  "name": "Authentik",
  "issuer": "https://auth.example.com/application/o/<app-slug>/",
  "client_id": "<your-client-id>",
  "client_secret": "<your-client-secret>",
  "scopes": "openid email profile"
}
```

## Keycloak

1. In the Keycloak admin console, select your realm and go to **Clients → Create client**.
2. **Client type:** OpenID Connect. **Client ID:** `bindery`.
3. Enable **Client authentication** (confidential client).
4. Under **Valid redirect URIs**, add: `https://bindery.example.com/api/v1/auth/oidc/keycloak/callback`.
5. Save, then go to the **Credentials** tab and copy the client secret.
6. To restrict login to a specific group, create a Keycloak group (e.g. `bindery-users`), assign users to it, and map it to the token via **Client scopes → groups** mapper.

Add provider in Bindery:

```json
{
  "id": "keycloak",
  "name": "Keycloak",
  "issuer": "https://keycloak.example.com/realms/<your-realm>",
  "client_id": "bindery",
  "client_secret": "<your-secret>",
  "scopes": "openid email profile groups",
  "allowed_groups": "/bindery-users"
}
```

The `issuer` must include the realm path. Keycloak groups in the token are prefixed with `/` (e.g. `/bindery-users`) — match this exactly in `allowed_groups`.

## JWKS caching

Bindery caches the IdP's JWKS (public keys) to avoid a round-trip to the identity provider on every token validation. The cache is refreshed automatically on cache miss or key rotation. This means:
- Token validation is fast and does not hit the IdP per-request.
- If you rotate IdP signing keys, Bindery will re-fetch on the next cache miss (typically within minutes).

## Provider load status and on-demand recovery

Bindery performs OIDC discovery (`<issuer>/.well-known/openid-configuration`) when providers are loaded — at startup and whenever an admin saves the provider list. If discovery fails for a provider (e.g. the IdP is briefly unreachable while Bindery's pod is starting), the provider is recorded as **failed** instead of being dropped silently. Two consequences:

1. `GET /api/v1/auth/oidc/providers` returns a `status` block per provider:

    ```json
    [
      {
        "id": "authentik",
        "name": "Authentik",
        "issuer": "https://auth.example.com/application/o/bindery/",
        "client_id": "...",
        "scopes": ["openid", "email", "profile"],
        "status": { "state": "ok" }
      },
      {
        "id": "google",
        "name": "Google",
        "issuer": "https://accounts.google.com",
        "client_id": "...",
        "scopes": ["openid", "email", "profile"],
        "status": {
          "state": "failed",
          "last_error": "oidc discovery for \"https://accounts.google.com\": context canceled",
          "last_attempt": "2026-05-05T22:25:42Z"
        }
      }
    ]
    ```

2. The **first login attempt** for a failed provider triggers an on-demand re-discovery, rate-limited to once per 30 seconds per provider. If discovery now succeeds, the provider transitions to `ok` and the login flow continues normally. If it still fails, the user gets the same `unknown oidc provider` error as before, and the failed entry's `last_error` and `last_attempt` are updated for the admin to inspect.

The login page only renders **Sign in with X** buttons for providers in `ok` state — failed providers are intentionally hidden so users don't click into a flow that's guaranteed to error.

## Multi-provider note: sub re-use

Two different IdPs can emit the same `sub` value for different users. Bindery's user mapping key is `(issuer, sub)`, not `sub` alone — so collisions across providers are impossible by design.

## Session and logout caveat

OIDC logout from the IdP does **not** immediately log the user out of Bindery. This is a known limitation.

Bindery issues its own HMAC-signed session cookie when OIDC login succeeds. That cookie is independent of the IdP session — revoking the IdP session, signing out of the IdP, or disabling the user in the IdP has no effect on the Bindery cookie until it expires naturally (~12 hours for a short-lived session, up to 30 days if "Remember me" is checked).

**Mitigations:**

- **Shorten cookie lifetime** — reduce the session TTL in **Settings → General → Security → Session lifetime** so stale sessions expire sooner.
- **Force global logout** — rotate the session secret in **Settings → General → Security → Rotate session secret**. This invalidates every active Bindery session immediately for all users. Use for security incidents.
- **Per-user logout** (not yet available) — a `sessions` table with per-session revocation is planned for a future release. Until then, global secret rotation is the only way to evict a specific user.

## Client secret storage

OIDC client secrets are stored as plaintext in the `settings` table — the same posture as indexer API keys and download client passwords. This is an accepted trade-off for a self-hosted application: all sensitive values are in one place, and protecting the database file protects everything.

**What this means in practice:**
- Anyone with read access to `bindery.db` can extract OIDC client secrets.
- Protect the database file: `chmod 600 /config/bindery.db`, ensure the volume mount is not world-readable, and restrict `kubectl exec` / `docker exec` access to the container.
- The Bindery backup API (`POST /api/v1/backup`) creates a copy of the database — treat backup files with the same care.

An env-var reference pattern (reading secrets from environment variables rather than storing them in the DB) is planned for a future release.

## Helm / Kubernetes

```yaml
# values.yaml
env:
  BINDERY_OIDC_REDIRECT_BASE_URL: "https://bindery.example.com"
```

Provider configuration lives in the database (Settings UI or `settings` table), not in the Helm chart, to avoid storing client secrets in values files.

## Rollback

Migration `018_oidc.sql` is additive-only (nullable columns). Rolling back the binary is safe — the columns remain in place and are ignored by older versions.

## See also

- [docs/troubleshooting-auth.md](troubleshooting-auth.md) — consolidated symptom→cause→fix table for all auth phases

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Browser loops between Bindery login and IdP without completing | `BINDERY_OIDC_REDIRECT_BASE_URL` mismatch — IdP receives a callback URL that doesn't match its registration | Set `BINDERY_OIDC_REDIRECT_BASE_URL` to the exact public URL your proxy exposes (including scheme, domain, and any path prefix). The value must match the redirect URI registered in your IdP character-for-character. See [Redirect URL construction](#redirect-url-construction). |
| `redirect_uri_mismatch` error from IdP on callback | Callback URL sent by Bindery does not match the URI registered in the IdP | Same cause as redirect loop. Confirm `BINDERY_OIDC_REDIRECT_BASE_URL` is set and correct. Copy the exact URL from the IdP error message and register it. |
| `state mismatch` or `nonce mismatch` error on callback | State/nonce cookie set during login redirect was lost or altered before the callback arrived | Two common causes: (1) reverse proxy strips `Set-Cookie` response headers — ensure `Set-Cookie` passes through unchanged; (2) login and callback served on different domains or subdomains — the session cookie won't be sent cross-domain. Both must use the same origin. With path-prefix deployments, verify the cookie `Path` attribute is not restricted. |
| `invalid_client` or `unauthorized_client` from IdP on callback | Client ID / secret mismatch, or redirect URI not registered | Verify the redirect URI in your IdP exactly matches `<BINDERY_OIDC_REDIRECT_BASE_URL>/api/v1/auth/oidc/<id>/callback`. Check client ID and secret. |
| Login button does not appear on login page | Provider not configured, or OIDC settings not saved | Check **Settings → Security → OIDC Providers**. Verify the provider record has `issuer`, `client_id`, `client_secret` set. |
| `issuer mismatch` in token validation | Bindery's configured `issuer` does not match the `iss` claim in the ID token | For Keycloak, issuer includes the realm: `https://keycloak.example.com/realms/<realm>`. For Authentik, it includes the app slug. Use `BINDERY_LOG_LEVEL=debug` to log the received `iss` value. |
| Two different real users map to the same Bindery account | Two providers emitting the same `sub` for different people | This cannot happen — Bindery keys on `(issuer, sub)`, not `sub` alone. If it does occur, file a bug. |
| OIDC logout from IdP doesn't log out of Bindery | Session cookie is independent of IdP session; cookie TTL is ~12h or up to 30d | Rotate session secret in **Settings → General → Security** to force global logout. Shorten session TTL to reduce window. Per-user revocation is planned for a future release. |
| `connection refused` or timeout fetching discovery URL | IdP not reachable from Bindery container/pod at startup | Check network policy / firewall. Bindery must reach `<issuer>/.well-known/openid-configuration`. Test with `curl` from inside the container. |
| JWKS fetch fails after IdP key rotation | Stale cached JWKS | Restart Bindery to force re-fetch. The cache auto-refreshes on miss in subsequent versions. |
| `allowed_groups` filter blocks all users | Group claim name or format differs from config value | Enable `BINDERY_LOG_LEVEL=debug` to log decoded ID token claims. Keycloak groups are prefixed with `/` (e.g. `/bindery-users`); match this exactly. |
| Login page shows "Contact your administrator for access" with no login form | `BINDERY_LOCAL_AUTH_ENABLED=false` and no OIDC providers configured | Either configure an OIDC provider or restart with `BINDERY_LOCAL_AUTH_ENABLED=true` to restore local login. |
| OIDC login returns 403 "account not provisioned" | `BINDERY_OIDC_AUTO_PROVISION=false` and no account pre-created for this OIDC identity | Admin must create an account for this user first (`POST /api/v1/auth/users`), then ask the user to log in again. Or set `BINDERY_OIDC_AUTO_PROVISION=true` to allow self-registration. |
| First OIDC login creates a duplicate account instead of linking to existing local account | `BINDERY_OIDC_EMAIL_LINK=false` (default) | Set `BINDERY_OIDC_EMAIL_LINK=true` to link by email on first login. After enabling, the next login attempt will link if the email matches. Existing duplicate account must be removed manually if already created. |
