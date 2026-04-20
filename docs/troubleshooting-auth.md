# Auth Troubleshooting

Quick-reference for every auth-related symptom across all three auth phases. Each row maps to a risk mitigation identified during design — the "fix" column is what the mitigation actually looks like at runtime.

For setup guides, see the per-feature docs linked at the bottom.

## Symptom → cause → fix

| Symptom | Cause | Fix |
|---------|-------|-----|
| Bindery refuses to start: `proxy mode requires BINDERY_TRUSTED_PROXY` | Proxy mode was enabled but `BINDERY_TRUSTED_PROXY` is empty — Bindery refuses to start rather than silently allow any IP to set the identity header | Set `BINDERY_TRUSTED_PROXY` to your proxy's IP or CIDR (e.g. `172.20.0.0/16` for Docker bridge, `10.0.0.0/8` for Kubernetes pods). Bindery logs the effective CIDR list at startup so you can verify it. |
| Any LAN host can forge `X-Forwarded-User: admin` and get in | `BINDERY_TRUSTED_PROXY` is too broad (e.g. `0.0.0.0/0`) or empty while mode is somehow `proxy` | Tighten to only your proxy's IP or pod subnet. Confirm effective CIDR in startup log: `trusted proxies: [...]`. Firewall rules should ensure Bindery is unreachable except via the proxy. |
| `401 Unauthorized` after successful SSO login | Source IP not in `BINDERY_TRUSTED_PROXY`, or identity header name mismatch | (1) Check startup log for trusted CIDR — the request source IP (Traefik/Caddy container, not browser) must match. (2) Confirm `BINDERY_PROXY_AUTH_HEADER` matches what the proxy sends: Authelia → `Remote-User`; Authentik → `X-Authentik-Username`. Enable `BINDERY_LOG_LEVEL=debug` to log both. |
| New Bindery user created every time a user renames their IdP account | Auto-provisioning keys on a mutable username/email header; rename produces a new row and orphans the old user's data | Use a stable, opaque identifier: Authelia internal UUID, Authentik user UUID, Keycloak `sub` claim. Configure `BINDERY_PROXY_AUTH_HEADER` to point at that header. Admin can merge orphaned users from **Settings → Users**. See [docs/auth-proxy.md — Header choice](auth-proxy.md#header-choice-stability-matters). |
| Two users at different IdPs map to the same Bindery account | Two providers can emit the same `sub` value for different people | This cannot happen by design — Bindery's user-mapping key is the composite `(oidc_issuer, oidc_sub)`, not `oidc_sub` alone. If you observe it, it is a bug; file an issue. |
| OIDC logout from IdP does not log out of Bindery | Bindery's HMAC-signed session cookie is independent of the IdP session; revoking the IdP session has no effect until the cookie TTL expires (~12h short-lived, up to 30d with "Remember me") | **Known limitation.** Mitigations: (1) shorten session TTL in Settings → General → Security; (2) rotate the session secret in Settings → General → Security → Rotate session secret — this immediately invalidates all active sessions. Per-user revocation is planned for a future release. |
| Rotating the session secret logs everyone out at once | The session secret is shared; rotating it is a global operation with no per-session granularity | Expected behaviour when used to force a global logout. Warn users before rotating. Per-session revocation is planned. |
| OIDC client secret visible to anyone with DB read access | Client secrets are stored as plaintext in the `settings` table — same posture as indexer API keys | **Known limitation.** Protect `bindery.db` with `chmod 600`; restrict `kubectl exec` / `docker exec` access; treat backup files as sensitive. An env-var secret-ref pattern is planned for a future release. |
| User B can see User A's authors or books via API | A repository query is missing the `userID` filter — this is a bug; the `QueryScope` helper in the repo layer should catch it | File a bug report with the exact endpoint URL and response body. As a workaround, ensure user B does not have the `admin` role (admins can access all users' data by design). The two-user integration test matrix is the backstop to detect regressions. |
| `403 Forbidden` on API mutations that worked before v1.0 | CSRF double-submit token is now required for session-cookie-authenticated mutations | Switch the caller to `X-Api-Key` auth (exempt from CSRF), or add a `GET /api/v1/auth/csrf` preflight: `TOKEN=$(curl -s -b bindery_session=... http://bindery:8787/api/v1/auth/csrf \| jq -r .token)` then pass `-H "X-CSRF-Token: $TOKEN"`. |
| Existing API-key scripts break after v1.0 CSRF rollout | CSRF token incorrectly required on `X-Api-Key` requests | This should not happen — CSRF only applies when auth is session-cookie-based; `X-Api-Key` requests bypass it. If it does occur, it is a bug; file an issue. |
| Migration `019_multiuser.sql` fails mid-run | Orphaned rows (broken foreign keys from earlier bugs) prevent the `NOT NULL owner_user_id` backfill | The migration runs in a transaction — the DB is not corrupted on failure. Check the startup log for the printed repair query (typically a `DELETE FROM downloads WHERE book_id NOT IN (...)`). Run it, then restart. Always take a backup before upgrading — see [docs/upgrade-v2.md](upgrade-v2.md). |
| All data missing after v1.0 upgrade | Migration ran against the wrong database file | Confirm `BINDERY_DB_PATH` points to the correct file. Restore from the pre-upgrade backup. |
| Admin locked out — no admin account reachable | All user rows have `role='user'`, or admin account was deleted | Direct DB repair (no restart needed): `sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE username='<name>';"` — Kubernetes: `kubectl exec deploy/bindery -- sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE id=1;"` |

## Per-feature docs

| Topic | Doc |
|-------|-----|
| Proxy SSO setup (Traefik/Authelia, Caddy/Authentik, nginx) | [docs/auth-proxy.md](auth-proxy.md) |
| OIDC setup (Google, GitHub, Authelia, Authentik, Keycloak) | [docs/auth-oidc.md](auth-oidc.md) |
| Multi-user roles, user management, CSRF tokens | [docs/multi-user.md](multi-user.md) |
| v1.0 upgrade: backup, dry-run, kubectl cp, rollback | [docs/upgrade-v2.md](upgrade-v2.md) |
| Wiki: step-by-step Authelia howto | [Wiki — Authelia forward-auth](https://github.com/vavallee/bindery/wiki/Howto-Authelia-proxy-auth) |
| Wiki: step-by-step Authentik howto | [Wiki — Authentik forward-auth](https://github.com/vavallee/bindery/wiki/Howto-Authentik-proxy-auth) |
| Wiki: proxy login troubleshooting walkthrough | [Wiki — Troubleshoot proxy login](https://github.com/vavallee/bindery/wiki/Howto-Troubleshoot-proxy-login) |
| Wiki: full troubleshooting reference | [Wiki — Troubleshooting](https://github.com/vavallee/bindery/wiki/Troubleshooting) |
