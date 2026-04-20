# Multi-User & v1.0 Upgrade Guide

Bindery v1.0 introduces per-user library scoping and CSRF hardening. Every author, book, download, and profile is now owned by a user. Existing single-user installs are automatically migrated — all data is assigned to the original admin user (user ID 1) and behaviour is unchanged.

## What changes

- Each user has their own authors, books, downloads, quality profiles, metadata profiles, and root folders.
- Two roles: `admin` (full access, manages users and system settings) and `user` (own library only).
- The first account created via `/setup` is `admin`. OIDC-auto-provisioned users get the `user` role unless their IdP group matches `allowed_admin_groups`.
- Admin endpoints (`GET/POST/DELETE /auth/users`, indexer/download client settings) require the `admin` role.
- CSRF protection upgraded from `X-Requested-With` header check to double-submit token (`GET /auth/csrf` + `X-CSRF-Token` header on mutations). API-key requests are exempt.

## Before you upgrade

**Take a database backup first. Migration 019 is a one-way door on SQLite.**

```bash
# Via API
curl -X POST -H "X-Api-Key: <key>" http://bindery:8787/api/v1/backup

# Via UI
Settings → General → Backup → Create backup
```

Bindery also runs a pre-migration integrity check that counts orphaned rows and aborts cleanly with a repair hint if any are found. Fix any reported orphans before proceeding.

**Rehearse on a snapshot (recommended for prod installs):**

```bash
# Copy your bindery.db to a test environment and boot the new binary against it
cp /config/bindery.db /tmp/bindery-test.db
BINDERY_DB_PATH=/tmp/bindery-test.db ./bindery
# Verify: all authors/books load under user 1 with zero data loss
```

## Upgrade steps

1. Stop the current Bindery process.
2. Backup (see above).
3. Replace the binary or update the image tag.
4. Start Bindery. Migration `019_multiuser.sql` runs automatically inside a transaction.
5. On success, startup logs emit `migration 019 complete; all rows backfilled to user_id=1`.
6. Verify: open the UI, confirm your library loads normally.

If migration fails (e.g. due to orphaned rows), Bindery logs the error and exits cleanly — the database is not left in a half-migrated state because the migration runs in a transaction.

## Single-user installs

No action required beyond the backup and upgrade steps above. Your library is transparently owned by user 1. You will see no behavioural change unless you add additional users.

## User management

### Creating users

Admin only. **Settings → Users → Add user**, or:

```
POST /api/v1/auth/users
{"username": "alice", "password": "...", "role": "user"}
```

OIDC users are auto-provisioned on first login and assigned the `user` role.

### Listing users

```
GET /api/v1/auth/users
```

Returns id, username, role, and last-seen timestamp. Passwords and OIDC credentials are not returned.

### Deleting users

```
DELETE /api/v1/auth/users/{id}
```

Deleting a user does not delete their library data. Reassign or clean up data from the admin UI before deleting.

### Roles

| Role | Can do |
|------|--------|
| `admin` | Everything: manage users, indexers, download clients, system settings, all users' data |
| `user` | Own library only: authors, books, downloads, profiles within their account |

## CSRF tokens

Bindery v1.0 replaces the `X-Requested-With` header check with a double-submit CSRF token for session-cookie-authenticated requests.

**For browser-based use:** the UI handles this automatically — no action required.

**For API scripts using session cookies:** fetch a token before mutations:

```bash
TOKEN=$(curl -s -b bindery_session=<cookie> http://bindery:8787/api/v1/auth/csrf | jq -r .token)
curl -X POST -b bindery_session=<cookie> -H "X-CSRF-Token: $TOKEN" ...
```

**For API scripts using `X-Api-Key`:** CSRF tokens are not required — API-key requests are exempt. Your existing scripts continue to work unchanged.

## Settings UI changes

Settings is now split into two tabs:

- **My Account** — API key, password, notification preferences (visible to all users)
- **Admin** — Indexers, download clients, quality/metadata profiles, users list (admin only)

## Rollback

Migration `019_multiuser.sql` cannot be trivially reversed on SQLite (SQLite does not support `DROP COLUMN`). Treat this upgrade as a one-way door.

**Rollback posture:** if you need to revert, restore from the pre-upgrade snapshot and run the previous binary. Data written after the upgrade is not recoverable from the snapshot.

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Migration fails at startup: `orphaned rows detected` | Rows in `downloads` or other tables with broken foreign keys from earlier bugs | Follow the repair hint in the log (a `DELETE` or `UPDATE` query is printed). Rerun migration after fixing. |
| Migration fails mid-run and Bindery exits | Unexpected schema state | The migration runs in a transaction — the database is not corrupted. Restore from backup, fix reported issue, retry. |
| All data missing after upgrade | Upgrade applied to wrong database file | Check `BINDERY_DB_PATH` in your config. Ensure the backup and the running DB are the same file. |
| User A can see User B's books via API | A repo method is missing `userID` filter (bug) | File a bug with the exact API endpoint. As a workaround, restrict access with the `user` role (not admin). |
| `403 Forbidden` on API mutation (was working before) | CSRF token now required for session-cookie requests | Switch to API-key auth (`X-Api-Key`), which is CSRF-exempt. Or fetch a token from `GET /auth/csrf`. |
| OIDC user auto-provisioned but should be admin | `allowed_admin_groups` not set for OIDC provider | Add the IdP group name to `allowed_admin_groups` in the OIDC provider config. Existing provisioned users need manual role update via `PUT /api/v1/auth/users/{id}`. |
| Cannot delete a user — data still present | Delete user API does not delete library data | Manually reassign or remove the user's authors/books first, then delete the user. |
| Session secret rotation logs everyone out | Expected behaviour when rotating to force global logout | Warn users before rotating. Session revocation per-user is planned for a future release. |
