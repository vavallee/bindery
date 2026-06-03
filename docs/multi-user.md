# Multi-User

Bindery v1.0 introduces per-user library scoping: authors, books, downloads, quality profiles, metadata profiles, and root folders can be owned by a specific user, and users log in with their own credentials.

> **Important — data isolation is opt-in.** Per-user data isolation is gated behind the `BINDERY_ENFORCE_TENANCY` environment variable, which **defaults OFF**. With it unset (the default), any authenticated user can see and manage *all* data — Bindery behaves like a single-user instance regardless of how many accounts exist. To make users see only their own library, set `BINDERY_ENFORCE_TENANCY=true`.
>
> When enforcement is on, Bindery scopes:
> - **Tier-2 join-scoped resources** — download queue, history, pending grabs, and the OPDS catalogue — to the requesting user.
> - **Per-user resources** — each user's own authors, books, profiles, root folders, API key, password, and notification preferences.
>
> Role-based gating of admin-only configuration (indexers, download clients, user management, system settings) applies in **both** modes — that does not depend on `BINDERY_ENFORCE_TENANCY`. The flag only controls whether *library data* is partitioned per user.

For upgrade instructions and migration steps, see [docs/upgrade-v1.md](upgrade-v1.md).

## Role model

Two roles exist: `admin` and `user`.

- The **first account** created through the `/setup` wizard is always `admin`.
- Users created by an admin via Settings → Users default to the `user` role.
- OIDC auto-provisioned users get the `user` role by default. To make the IdP authoritative for the admin role, set `BINDERY_OIDC_ADMIN_GROUP` so users in that IdP group are promoted automatically; see [OIDC role mapping](auth-oidc.md#oidc-role-mapping) in [docs/auth-oidc.md](auth-oidc.md).
- An admin can change any user's role at any time: **Settings → Users → Edit**, or `PUT /api/v1/auth/users/{id}/role` with `{"role": "admin"}`.

### Capability matrix

| Action | `admin` | `user` |
|--------|:-------:|:------:|
| View and manage own authors/books/downloads | Yes | Yes |
| View and manage own quality/metadata profiles | Yes | Yes |
| View and manage own root folders | Yes | Yes |
| Change own password and API key | Yes | Yes |
| Configure own notification preferences | Yes | Yes |
| View other users' library data | Yes | No |
| Manage other users' library data | Yes | No |
| Create, edit, delete users | Yes | No |
| Change user roles | Yes | No |
| Configure indexers | Yes | No |
| Configure download clients | Yes | No |
| Configure system-wide settings | Yes | No |
| View admin settings tabs in UI | Yes | No |
| Trigger system-level operations (backup, scan, migrate) | Yes | No |

## User management

### Creating a user

**Settings → Users → Add user** (admin only), or via API:

```bash
curl -X POST http://bindery:8787/api/v1/auth/users \
  -H "X-Api-Key: <admin-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"username": "alice", "password": "correct-horse-battery", "role": "user"}'
```

OIDC users are created automatically on first login — no pre-creation needed unless `BINDERY_OIDC_AUTO_PROVISION=false`.

### Listing users

```bash
curl http://bindery:8787/api/v1/auth/users \
  -H "X-Api-Key: <admin-api-key>"
```

Returns: `[{"id": 1, "username": "admin", "role": "admin", "last_seen": "..."}]`. Passwords and OIDC credentials are never returned.

### Updating a user

```bash
# Promote to admin
curl -X PUT http://bindery:8787/api/v1/auth/users/2/role \
  -H "X-Api-Key: <admin-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"role": "admin"}'
```

### Deleting a user

```bash
curl -X DELETE http://bindery:8787/api/v1/auth/users/2 \
  -H "X-Api-Key: <admin-api-key>"
```

Deleting a user does **not** delete their library data. Authors, books, and downloads owned by that user remain in the database but become inaccessible through the normal UI. Reassign or remove the user's data before deleting:

1. Go to **Settings → Users → [user] → Library** to view their content.
2. Delete or reassign authors and books as needed.
3. Then delete the user.

## Settings UI layout

Settings is split into two tabs post-v1.0:

- **My Account** — API key, password change, notification preferences. Visible to all users.
- **Admin** — Indexers, download clients, quality profiles, metadata profiles, users list, system settings. Visible to `admin` role only. Non-admins who navigate to an admin URL receive a 403.

## CSRF tokens

v1.0 replaces the `X-Requested-With` header check with a proper double-submit CSRF token on all session-cookie-authenticated mutations.

**Browser users:** the UI handles this transparently.

**API scripts using session cookies:** fetch a token first:

```bash
TOKEN=$(curl -s -b "bindery_session=<value>" \
  http://bindery:8787/api/v1/auth/csrf | jq -r .token)

curl -X POST http://bindery:8787/api/v1/author \
  -b "bindery_session=<value>" \
  -H "X-CSRF-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "Ursula K. Le Guin"}'
```

**API scripts using `X-Api-Key`:** CSRF is not required — API-key requests bypass the check entirely. Existing automation continues to work without changes.

## See also

- [docs/troubleshooting-auth.md](troubleshooting-auth.md) — consolidated symptom→cause→fix table for all auth phases
- [docs/DEPLOYMENT.md#environment-variables](DEPLOYMENT.md#environment-variables) — `BINDERY_ENFORCE_TENANCY` and other runtime knobs

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| User B can see User A's authors or books via the API | `BINDERY_ENFORCE_TENANCY` is unset — this is the documented default behaviour, not a bug | This is expected when tenancy enforcement is off: all authenticated users share one library view. To partition library data per user, set `BINDERY_ENFORCE_TENANCY=true` and restart. (Admins can access all users' data regardless of this flag, by design.) |
| Data remains after a user is deleted | `DELETE /auth/users/{id}` does not cascade to library data | Reassign or delete the user's authors and books before deleting the user account (see "Deleting a user" above). |
| `403 Forbidden` on an API call that worked before v1.0 | Session-cookie mutations now require `X-CSRF-Token` | Switch callers to `X-Api-Key` auth (CSRF-exempt), or add a `GET /auth/csrf` preflight to your script (see "CSRF tokens" above). |
| Admin locked out — no admin account exists or all admins deleted | User row has `role='user'` or all admin rows were removed | Recover via direct DB update (no Bindery restart needed if you can write to the DB file): `sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE username='<your-username>';"` — or in Kubernetes: `kubectl exec deploy/bindery -- sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE id=1;"` |
| OIDC user auto-provisioned as `user` but should be `admin` | `BINDERY_OIDC_ADMIN_GROUP` not set, so the IdP group is never consulted | Set `BINDERY_OIDC_ADMIN_GROUP` to the IdP group name (and `BINDERY_OIDC_GROUP_CLAIM` if your IdP emits groups under a non-default claim). To promote a single existing account without group mapping: `PUT /api/v1/auth/users/{id}/role` with `{"role": "admin"}`. |
| Non-admin user can reach admin settings page in UI | Browser cached a pre-v1.0 session or route bundle | Hard-refresh the page (`Ctrl+Shift+R`). If it persists, log out and back in. The backend enforces role checks regardless of what the UI renders. |
