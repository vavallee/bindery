# Multi-User

Bindery v1.0 introduces per-user library scoping: authors, books, downloads, quality profiles, and metadata profiles can be owned by a specific user, and users log in with their own credentials.

> **Root folders are not per-user.** They are a single shared, admin-managed pool (Settings → Root Folders), and import destinations resolve per-author plus a global default — there is no per-user library directory today. Regular users don't get their own root folders. Per-user root folders are a tracked future enhancement, not current behaviour.

> **Important — data isolation is opt-in.** Per-user data isolation is gated behind the `BINDERY_ENFORCE_TENANCY` environment variable, which **defaults OFF**. With it unset (the default), any authenticated user can see and manage *all* data — Bindery behaves like a single-user instance regardless of how many accounts exist. To make users see only their own library, set `BINDERY_ENFORCE_TENANCY=true`.
>
> When enforcement is on, Bindery scopes:
> - **Tier-2 join-scoped resources** — download queue, history, pending grabs, and the OPDS catalogue — to the requesting user.
> - **Per-user resources** — each user's own authors, books, quality and metadata profiles, and password. (Root folders are **not** per-user; see the note above. The API key and the notification webhooks are instance wide and admin only.)
> - **Background Hardcover list syncs** to the list's owner: a list decides whether to create a book by reading only the rows its owner can see, so one user's list never skips, widens or re-opens another user's book and never reuses another user's author. Rows with no owner stay shared and reusable by every list. Because a book's and an author's Hardcover id is unique across the whole instance, a work another user already holds cannot be created a second time; the sync leaves that row untouched, logs whose it is, and counts the book as skipped.
>
> **Admins see everything in list views.** With enforcement on, an `admin` is never filtered by ownership: the authors and books list endpoints (and the OPDS feed) return *all* users' libraries plus unowned/global rows, the same way an admin can already open any single item by ID. This is a **shared library across admins**, by design: it does not widen access, it makes lists consistent with per-item access. Non-admin (`user`) accounts stay isolated to their own rows plus unowned/global rows. Requests authenticated by API key, and requests the auth mode admits without a login (every request in `disabled` mode, local clients in `local-only` mode), act as the administrator: they carry the admin role and the first admin account's id, so they are likewise unscoped and anything they create is owned by that admin.
>
> Role-based gating of admin-only configuration (indexers, download clients, user management, system settings) applies in **both** modes — that does not depend on `BINDERY_ENFORCE_TENANCY`. The flag only controls whether *library data* is partitioned per user.
>
> Bindery logs a warning at startup when it sees more than one account with the flag off, so the combination is at least visible to whoever added the second user. Sharing one library between accounts is a supported setup; the warning exists because nothing else says which one you are running.

> **Choosing an auth mode: `local-only` requires `BINDERY_TRUSTED_PROXY` behind a proxy.** In `local-only` mode any client whose resolved IP is private is served with admin rights and no login. Bindery resolves that IP from the TCP peer unless `BINDERY_TRUSTED_PROXY` names the proxies whose `X-Forwarded-For` it may trust, so behind a reverse proxy or a Kubernetes ingress the peer is the proxy's own private address and every proxied request qualifies. Set `BINDERY_TRUSTED_PROXY` to your proxy's IP or CIDR, or pick `enabled` (or `proxy`) mode. Bindery logs a warning at startup and on a mode change when it sees this combination. An instance reached directly on a LAN with no proxy in front is unaffected.

For upgrade instructions and migration steps, see [docs/upgrade-v1.md](upgrade-v1.md).

## Role model

Three roles exist: `admin`, `user` and `requester`.

- The **first account** created through the `/setup` wizard is always `admin`.
- Users created by an admin via the **Users** page (the people icon in the header) default to the `user` role.
- OIDC auto-provisioned users get the `user` role by default, or whatever `BINDERY_OIDC_DEFAULT_ROLE` says (`requester` is supported). To make the IdP authoritative for the admin role, set `BINDERY_OIDC_ADMIN_GROUP` so users in that IdP group are promoted automatically; see [OIDC role mapping](auth-oidc.md#oidc-role-mapping) in [docs/auth-oidc.md](auth-oidc.md).
- Proxy auth provisions `user`. There is no header to role mapping; an admin sets the role after the account exists (see [docs/auth-proxy.md](auth-proxy.md#roles)).
- An admin can change any user's role at any time: the role select on the Users page, or `PUT /api/v1/auth/users/{id}/role` with `{"role": "admin"}`, `{"role": "user"}` or `{"role": "requester"}`. The last admin cannot be demoted to either other role.

### Capability matrix

| Action | `admin` | `user` | `requester` |
|--------|:-------:|:------:|:-----------:|
| View and manage own authors/books/downloads | Yes | Yes | No |
| View and manage own quality/metadata profiles | Yes | Yes | No |
| Manage root folders (single shared/global pool) | Yes | No | No |
| Change own password | Yes | Yes | Yes |
| Read or rotate the instance API key | Yes | No | No |
| Configure notification webhooks | Yes | No | No |
| View other users' library data | Yes | No | Titles only, read only (see [Requester](#requester)) |
| Manage other users' library data | Yes | No | No |
| Search metadata providers | Yes | Yes | Yes, rate limited |
| Ask for a book or an author | Yes | Yes | Yes |
| Approve or decline requests | Yes | No | No |
| Download book files, use OPDS | Yes | Yes | No |
| Create, edit, delete users | Yes | No | No |
| Change user roles | Yes | No | No |
| Configure indexers | Yes | No | No |
| Configure download clients | Yes | No | No |
| Configure system-wide settings | Yes | No | No |
| View admin settings tabs in UI | Yes | No | No |
| Trigger a backup or a migration import | Yes | No | No |
| Start a library scan | Yes | Yes | No |
| See server filesystem paths (storage health, path settings, last library scan) | Yes | No | No |

## Requester

A requester asks for books; an admin decides. It is the role for people who share an instance but should not grab, delete or configure anything: family, friends, a book club.

**What a requester can do**

- Browse the library read only. The Library page lists each book's title, author, series, cover, status and which formats are on disk. It comes from `GET /api/v1/requests/library`, a projection built field by field, so it carries no file paths, owner ids, provider ids or links into book pages.
- Search the metadata providers from the Request page, the same search the Add dialog runs. Those searches spend provider quota, so each requester is limited to a burst of 20 and then one every 3 seconds; creating a request looks the item up too and spends the same allowance. Cover images have a separate, larger allowance.
- Request a book or an author, choosing only the format. Bindery looks the item up itself and stores the title and author the provider reports; nothing else the requester sends is kept. A request for something already in the library, or already requested by the same person, is refused with a sentence saying so.
- Follow their own requests on My requests: waiting, approved, available once a file is imported (for an author, how many of the author's books are in), or declined with the admin's reason. A pending request can be withdrawn.
- Change their own password, and sign in and out.

**What a requester cannot do**

- Add, grab, search indexers, delete, edit or refresh anything, or see the queue, history, blocklist, wanted list, calendar, series or recommendations.
- Open book or author pages, download a file, or use OPDS (OPDS answers 403 for a requester whether they sign in with a cookie or with Basic credentials).
- See settings, profiles, root folders, indexers, download clients, notifications, users or system pages, or read the API key.
- Have more than 25 requests waiting at once. An admin can change that with the `requests.max_pending_per_user` setting.

These limits are enforced on the server, not just hidden in the UI. Every API route a requester may call is on one allow list (`auth.RequesterAllowList`); any other route, including one added in a later release, answers 403. The list is checked against the path after `BINDERY_URL_BASE` is removed, and a path that is encoded, doubled or dotted in a way that could route differently is refused rather than interpreted.

**Approving a request.** Admins see **Requests** in the nav with a count of pending requests. Approve opens the same choices as adding by hand (metadata profile, root folder, monitoring and format for an author; format and search on add for a book), prefilled from the instance defaults. Bindery then runs the ordinary add with the requester as the owner, so the new author and books belong to the requester. Two admins approving the same request at once produce one add; the second is told it was already decided. Decline takes an optional reason, which the requester sees. Turn on the **Request** toggle on a webhook in Settings, Notifications to hear about new requests; it is off for every existing webhook.

**Auto-approving one account.** An admin can skip the queue for a single requester with the checkbox in the **Auto-approve requests** column on the Users page, or `PUT /api/v1/auth/users/{id}/auto-approve` with `{"enabled": true}`. It is off by default and off for every account that has never had it set. With it on, a request from that account runs the same claim and add an admin's Approve runs, straight away and with no deciding user recorded, so the already-in-the-library check, the stored-payload revalidation and the pending cap all still apply. A book request searches on add; an author request runs the ordinary catalogue sync. If the add fails the request stays pending in the queue rather than being lost.

The account gets the same `requests.max_pending_per_user` limit as a daily ceiling: once it has had that many requests auto-approved since midnight UTC, the next one is left pending for a human, and the counter resets the next day. The limit defaults to 25. An auto-approved request does not fire the Request webhook, so an admin is not pinged for an item that was added without them; a request left pending, whether the add failed or the day's quota is spent, still notifies. The switch only affects the next request: anything already waiting stays waiting for a human.

**Requester restrictions hold in every auth mode.** `disabled` and `local-only` mode serve an anonymous caller (or, in `local-only`, any client on a private network) as the admin, but a request carrying a requester's own session is never elevated that way: it acts as the requester, and so does its OPDS access. Two things still act as the admin whatever cookie rides along: the API key, and, in those two modes, a caller who simply signs out, because the mode itself admits them. So a requester account only means something when the person cannot reach Bindery without signing in, which in practice is `enabled` or `proxy` mode.

New requests notify the **Request** webhook at most once an hour for the same item from the same person, so withdrawing and asking again does not repeat the alert, and at most 10 times an hour per requester; past that the requests are still stored and shown under Requests, only the webhook is skipped. Titles, authors and usernames in that webhook have mentions, Slack escapes, markdown links and link schemes neutralised, so nothing in them is clickable or pings a channel.

**Tenancy.** With `BINDERY_ENFORCE_TENANCY` off (the default), a requester's Library lists every book in the instance, whoever added it, and approved requests are simply part of the shared library. With it on, the Library lists the requester's own books plus unowned ones, which after an approval means what they asked for. Either way a requester only ever sees their own requests, and a request for a book another user already has is refused as already in the library, because a book can only be in the library once.

## User management

### Creating a user

**Users → Add User** (admin only, via the people icon in the header), or via API:

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

Returns: `[{"id": 1, "username": "admin", "role": "admin", "createdAt": "2026-01-01T00:00:00Z"}]`, with `email` and `displayName` present only when the account has them. Passwords and OIDC credentials are never returned.

### Updating a user

```bash
# Promote to admin
curl -X PUT http://bindery:8787/api/v1/auth/users/2/role \
  -H "X-Api-Key: <admin-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"role": "admin"}'
```

```bash
# Make a requester
curl -X PUT http://bindery:8787/api/v1/auth/users/3/role \
  -H "X-Api-Key: <admin-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"role": "requester"}'
```

### Deleting a user

```bash
curl -X DELETE http://bindery:8787/api/v1/auth/users/2 \
  -H "X-Api-Key: <admin-api-key>"
```

Deleting a user does **not** delete their library data. Authors, books, and downloads owned by that user remain in the database but become inaccessible through the normal UI. Reassign or remove the user's data before deleting:

1. While signed in as an admin, review the account's content from the regular **Authors** and **Books** pages — admin list views are never ownership-filtered, so every user's rows are visible there.
2. Delete anything that should not be kept.
3. Then delete the user.

## Settings UI layout

Everyone sees the **General** tab (appearance, and the Security section, which is where a user changes their own password) and **About**. The API key, the session secret rotation, downloads, file naming, storage and backup render inside General for admins only. The remaining tabs are admin-only, in four groups:

- **Sources** — Indexers, Download Clients
- **Library** — Quality Profiles, Metadata Profiles, Root Folders
- **Integrations** — Notifications, Calibre, Audiobookshelf, Grimmory, API Keys
- **System** — Import / Migrate, Blocklist, Logs

Non-admins who open an admin tab are redirected back to General; admin API routes return 403. Inside General itself, a non admin sees Appearance and Security; the sections that describe or configure the server (file naming, downloads, search, the default library location, storage, the library scan panel and the schedule intervals) render for admins only, and the routes behind them, including `GET /system/storage` and `GET /library/scan/status`, answer 403 to anyone else. Users are managed on the dedicated **Users** page (the people icon in the header), not inside Settings.

## CSRF tokens

Session cookie mutations pass two guards: the `X-Requested-With: bindery-ui` header, and a double submit `X-CSRF-Token`. Both must be present. v1.0 added the token alongside the header check rather than replacing it.

**Browser users:** the UI handles this transparently.

**API scripts using session cookies:** fetch a token first:

```bash
TOKEN=$(curl -s -b "bindery_session=<value>" \
  http://bindery:8787/api/v1/auth/csrf | jq -r .csrfToken)

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
| In `local-only` mode, requests from outside the LAN are served without a login | Bindery is behind a reverse proxy or ingress and `BINDERY_TRUSTED_PROXY` is unset, so the proxy's own private address is taken as the client IP | Set `BINDERY_TRUSTED_PROXY` to the proxy's IP or CIDR so the real client IP is resolved from `X-Forwarded-For`, or switch to `enabled` mode. The startup log carries the same warning. |
| Data remains after a user is deleted | `DELETE /auth/users/{id}` does not cascade to library data | Reassign or delete the user's authors and books before deleting the user account (see "Deleting a user" above). |
| `403 Forbidden` on an API call that worked before v1.0 | Session-cookie mutations now require `X-CSRF-Token` | Switch callers to `X-Api-Key` auth (CSRF-exempt), or add a `GET /auth/csrf` preflight to your script (see "CSRF tokens" above). |
| Admin locked out — no admin account exists or all admins deleted | User row has `role='user'` or all admin rows were removed | Recover via direct DB update (no Bindery restart needed if you can write to the DB file): `sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE username='<your-username>';"` — or in Kubernetes: `kubectl exec deploy/bindery -- sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE id=1;"` |
| OIDC user auto-provisioned as `user` but should be `admin` | `BINDERY_OIDC_ADMIN_GROUP` not set, so the IdP group is never consulted | Set `BINDERY_OIDC_ADMIN_GROUP` to the IdP group name (and `BINDERY_OIDC_GROUP_CLAIM` if your IdP emits groups under a non-default claim). To promote a single existing account without group mapping: `PUT /api/v1/auth/users/{id}/role` with `{"role": "admin"}`. |
| Non-admin user can reach admin settings page in UI | Browser cached a pre-v1.0 session or route bundle | Hard-refresh the page (`Ctrl+Shift+R`). If it persists, log out and back in. The backend enforces role checks regardless of what the UI renders. |
