# Multi-User

Bindery v1.0 introduces per-user library scoping: authors, books and downloads can be owned by a specific user, and users log in with their own credentials. Quality and metadata profiles are shared across the instance and managed by admins.

> **Root folders are not per-user.** They are a single shared, admin-managed pool (Settings → Root Folders), and import destinations resolve per-author plus a global default — there is no per-user library directory today. Regular users don't get their own root folders. Per-user root folders are a tracked future enhancement, not current behaviour.

> **Important — data isolation is opt-in.** Per-user data isolation is gated behind the `BINDERY_ENFORCE_TENANCY` environment variable, which **defaults OFF**. With it unset (the default), any authenticated user can see and manage *all* data — Bindery behaves like a single-user instance regardless of how many accounts exist. To make users see only their own library, set `BINDERY_ENFORCE_TENANCY=true`.
>
> When enforcement is on, Bindery scopes:
> - **Tier-2 join-scoped resources** — download queue, history, pending grabs, and the OPDS catalogue — to the requesting user.
> - **History** by the account each entry belongs to. An entry belongs to the owner of its book, or when the book has no owner, to the owner of the download or the person who made the change. It keeps that owner after the book is deleted, so a deleted book's grabs and imports stay in its owner's history and nobody else's. An entry with no owner is shown to a user who is not an admin only while its book exists and is one they can see; an entry with neither an owner nor a book (such as history recorded before this rule for a book since deleted) is shown to admins only. Only the entry's owner or an admin can delete it or blocklist its release, and only an admin can for an entry with no owner. Deleting a user removes their history with a purge and hands it to the new owner with a reassign.
> - **Refresh all authors.** For a user who is not an admin, Refresh all refreshes only the authors they own and leaves shared (unowned) authors to an admin. Its progress is that user's own run: counts cover their authors only, and they never see another account's run. An admin's Refresh all still covers every author.
> - **Discover recommendations.** For a user who is not an admin, recommendations are built from their own books and authors plus shared ones, and only ever suggest a book they could already see. The Hardcover want to read list behind the instance's Hardcover token feeds an admin's recommendations only.
> - **Per-user resources** — each user's own authors, books, and password. (Root folders are **not** per-user; see the note above. Quality and metadata profiles are not per user either: every user and admin sees the same profiles, and only admins create, edit or delete them. The API key and the notification webhooks are instance wide and admin only.)
> - **Background Hardcover list syncs** to the list's owner: a list decides whether to create a book by reading only the rows its owner can see, so one user's list never skips, widens or re-opens another user's book and never reuses another user's author. Rows with no owner stay shared and reusable by every list. Because a book's Hardcover id is unique across the whole instance, a work another user already holds cannot be created a second time; the sync leaves that row untouched, logs whose it is, and counts the book as skipped. An author another user holds is no reason to skip: the list owner gets their own row for that author.
> - **Adding books and authors** by hand, from a recommendation, or through an author's catalogue sync. For the same reason a work another user already holds cannot be added again: Add Book answers 409 "book is held by another user" before it creates or fetches anything, and the answer says nothing else about that row. Matching an added book against a title already under a shared (unowned) author only considers rows the caller owns, plus, for an admin, rows that have no owner, so an add never adopts or changes a row another user owns, and a user who is not an admin never adopts a shared one either: they get a row of their own. A book a user who is not an admin adds by hand under a shared author belongs to them, not to everyone; an admin's stays unowned as before. For a user who is not an admin, an author alias only matches an author they can see, and a recommendation is only filed under their own or a shared author and belongs to them; an admin, who sees every library, still matches any, and a book an admin adds from a recommendation stays unowned as before. A catalogue sync only re-links a book whose owner matches the author it is syncing, so it never moves a book between a user's library and a shared author or another user's.
>
> **Shared rows are read only for users.** A row with no owner (an author or book an admin added, one an ABS, Calibre, Readarr or Goodreads import created, or one from before per user ownership) is shared: with enforcement on, every user can see it, but only an admin can change it. A `user` who tries to edit, delete, merge, refresh or re-link a shared author, or edit, delete, rebind, exclude or replace the cover or file of a shared book, gets the same 404 as for another user's row, from the single item routes and the bulk actions alike. The same holds for automatic downloads and pending releases for a shared book: they are not in a user's queue or pending list, and a user cannot delete, retry or grab them by id. Adding a book under a shared author still works, because it writes the new book, which belongs to the user, not the author. The add paths do not change shared rows for a user either: adding an author whose name matches a shared author answers 409 naming that author instead of re-linking it to the provider, adding a book does not adopt a shared book with the same title (the user gets a book of their own), and removing their own download with "unmonitor book" leaves a shared book monitored. One shared update does still happen on a user's add: a shared placeholder author created from a DNB record with only a name is linked to the provider's author id when someone adds a book by that author, so every user keeps adding under the one author. Another account's own placeholder is never touched. That link replaces only its id, name and description, never its owner, monitoring, profiles or root folders. A user also cannot delete one of their own authors, or merge it into another, while a book under it belongs to someone else or to nobody: both would take that book with it, so the request answers 409 and an admin has to do it.
>
> **A shared author's other rows stay private.** An author can hold books from several accounts' libraries, so with enforcement on a `user` only ever reaches their own books plus unowned ones through that author, never another account's. That covers the books list with `includeExcluded=true`, the author's duplicate review and the library wide duplicate review (a group with fewer than two of the caller's own books is not listed), and the author's series list (a series linked only through someone else's books is left out). The author level actions that rewrite every book under an author (editing monitor settings with "apply to existing books", the genres action, and the bulk set media type, monitor, set monitor mode and search actions) change or search only the books the caller may write, skip the rest without an error, and never touch another account's or a shared book. Admins, API key requests and installs with enforcement off are unchanged and still reach every book.

> **Each account holds its own author; a book is held once.** With `BINDERY_ENFORCE_TENANCY` on, two accounts can follow the same author: when alice has Frank Herbert and bob adds him, or adds a Herbert book nobody holds, bob gets his own Frank Herbert with his own monitoring, profiles and root folders, and each account sees only its own (plus shared ones with no owner). An author is unique per owner and provider id (`authors` and `author_identifiers`, since migration 130; a shared author counts as one owner). An account that can already see the author, its own or a shared one, still gets 409 "author already exists" naming that author. Admins see every library, so an admin adding an author another account holds gets that 409 too, as before. Books are still unique across the whole instance (`books.foreign_id`, `book_identifiers.foreign_id`): adding a book another account already holds answers 409 "book is held by another user", as above, and an author's catalogue sync skips the works another account holds, so bob's Herbert lists only the Herbert books nobody else has. Each account's copy of an author is refreshed on its own, so a popular author is fetched once per account that follows it. Alias names (`author_aliases.name`, migration 009) are still unique across the instance, so an alternate spelling recorded for alice's Herbert is not recorded again for bob's. Deleting an account whose library is reassigned, or purged while another account's books sit under its authors, folds an author into the receiving account's own row for the same author when it has one.
>
> With enforcement off nothing changes: the library is shared, an author has one row whoever added it, and a second account adding it gets the same 409 as before. Turning enforcement off after accounts have added their own copies keeps those copies; merge them by hand if you want one.

> **Admins see everything in list views.** With enforcement on, an `admin` is never filtered by ownership: the authors and books list endpoints (and the OPDS feed) return *all* users' libraries plus unowned/global rows, the same way an admin can already open any single item by ID. This is a **shared library across admins**, by design: it does not widen access, it makes lists consistent with per-item access. Non-admin (`user`) accounts stay isolated to their own rows plus unowned/global rows. Requests authenticated by API key, and requests the auth mode admits without a login (every request in `disabled` mode, local clients in `local-only` mode), act as the administrator: they carry the admin role and the first admin account's id, so they are likewise unscoped and anything they create is owned by that admin. The one exception is a request made with `forUser` (see **Requests made for another account** under [Requester](#requester)), which is owned by the account it names.
>
> Role-based gating of admin-only configuration (indexers, download clients, user management, system settings) applies in **both** modes — that does not depend on `BINDERY_ENFORCE_TENANCY`. The flag only controls whether *library data* is partitioned per user.
>
> Bindery logs a warning at startup when it sees more than one account with the flag off, so the combination is at least visible to whoever added the second user. Sharing one library between accounts is a supported setup; the warning exists because nothing else says which one you are running.

> **Choosing an auth mode: `local-only` requires `BINDERY_TRUSTED_PROXY` behind a proxy.** In `local-only` mode any client whose resolved IP is private is served with admin rights and no login. Bindery resolves that IP from the TCP peer unless `BINDERY_TRUSTED_PROXY` names the proxies whose `X-Forwarded-For` it may trust, so behind a reverse proxy or a Kubernetes ingress the peer is the proxy's own private address and every proxied request qualifies. Set `BINDERY_TRUSTED_PROXY` to your proxy's IP or CIDR, or pick `enabled` (or `proxy`) mode. Bindery logs a warning at startup and on a mode change when it sees this combination. An instance reached directly on a LAN with no proxy in front is unaffected.

> **The Calibre delivery queue is install wide.** There is one Calibre target per install, so the queue under `/api/v1/calibre/deliveries` is admin only and not scoped by owner: it holds every user's ebooks. The same goes for the Calibre bridge pull routes under `/bridge/v1`: whoever holds the Calibre plugin key can list and download every queued book, whichever account imported it. Treat that key like an admin credential. See [API.md](API.md#calibre-bridge-pull).

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
| View shared authors/books (no owner) | Yes | Yes | Titles only, read only |
| Change or delete shared authors/books (no owner) | Yes | No, with tenancy on | No |
| View quality/metadata profiles (shared across the instance) | Yes | Yes | No |
| Create, edit, delete quality/metadata profiles | Yes | No | No |
| View or remove blocklist entries | Yes | No | No |
| Blocklist a release from own history | Yes | Yes | No |
| Manage root folders (single shared/global pool) | Yes | No | No |
| Change own password | Yes | Yes | Yes |
| Read or rotate the instance API key | Yes | No | No |
| Configure notification webhooks | Yes | No | No |
| View other users' library data | Yes | No | Titles only, read only (see [Requester](#requester)) |
| Manage other users' library data | Yes | No | No |
| Search metadata providers | Yes | Yes | Yes, rate limited |
| Grab a release from indexer search results | Yes | Yes | No |
| Grab a download URL no search returned (API) | Yes | No | No |
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
| Read stored settings (`GET /api/v1/setting`) | Yes, all but secrets | Only the few a user screen reads (see below) | No |

### Grabbing as a user

A `user` account grabs releases from the search results Bindery showed it. `POST /api/v1/queue/grab` looks the release up by its `guid` among recent search results and sends the download URL the server recorded for it, credentials included, whatever `nzbUrl` the request carries. That applies to every caller: an admin grabbing a recent search result also gets the recorded URL. A release no recent search returned, or one whose results have aged out (24 hours, or a restart), is refused for a user account with a message to run the search again. Admins, API key requests and requests the auth mode admits as the admin fall back to the `nzbUrl` they posted.

With `BINDERY_ENFORCE_TENANCY` on, a grab's `bookId` must be a book the caller owns (otherwise 404, as for any other book route), and a release whose queue row belongs to another user answers 409 without describing that row. A grab claims another user's row only on the terms the scheduler's automatic grab uses: a failed row once it has been dead for six hours, or an imported row whose book was deleted. A claimed row is reset completely, owner and book included. Rows in `importBlocked` and live rows are never claimed this way.

A release grabbed without a `bookId` (the Search page does not tie a release to a book) is matched to a book when it finishes, from the file's embedded metadata, its file name and the release title. With `BINDERY_ENFORCE_TENANCY` on and a `user` account owning the download, that match only considers the user's own books and books with no owner. If none of those matches, the download stays unmatched in the queue. A download owned by an admin, or with no owner, is matched against the whole library, as is every download with tenancy off.

A blocklist entry filed for one book blocks that release for that book only, so a release blocklisted for one user's book (a grab or import blocklisted from its History, or a format or language its profile refused at import) does not stop another user's book from grabbing it. A release the download client failed as broken or reported stalled stays blocked for every book, since it is broken for everyone, and so does a failed or stalled download any user blocklists from their own History, as before. Blocklisting from your own History says "already blocklisted" only when an entry already blocks what yours would, so it does not reveal another user's entry for their own book.

### Downloads, queue messages and shared folders

An audiobook stored as a folder downloads as a zip, from the web app and over OPDS. The zip holds the folder's files except any file or subfolder another book tracks, matched without regard to ASCII case (letters outside ASCII must match exactly). Untracked files in the folder, such as cover art, cue sheets and nfo files, are included. This applies in both tenancy modes.

Download client errors are admin detail, like the rest of the download client configuration. In `GET /api/v1/queue`, a `staleClients` entry gives a non admin the client's name and the message `download client unreachable`, and a row whose release the client refused or could not be reached for shows a non admin `failed to send the release to the download client` as its `errorMessage`. Admins get the client's own error in both places. A `downloadFailed` History entry and the failure notification for such a grab carry that short message, with a hint about the kind of network failure, for every account; the client's own error is in the log. Older History entries that hold a client error show the short message to non admins. This is decided by role, so it applies in both tenancy modes.

With `BINDERY_ENFORCE_TENANCY` on, an import whose destination path is already tracked by a book owned by someone other than the download's owner is blocked with a message that another book in the library uses the path, without naming that book. When the same account owns both, or tenancy is off, the message names the book and points at Fix match.

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

**Requests made for another account.** A request front end that keeps its own users (a Seerr-style app talking to Bindery with the API key) can make each request as the person who asked, rather than as the first admin. `POST /api/v1/requests` accepts a `forUser` user id from an admin session or the API key, and from nobody else: any other caller that sends it gets 403. That includes a caller that `disabled` or `local-only` mode admits as admin without signing in, since there would be nobody to record as having asked; such an integration should send the API key. An admin who names their own id just makes their own request. The request is then that account's in every way: they own it, it counts against their `requests.max_pending_per_user`, it shows on their My requests, and an approval adds the books as theirs. Their **Auto-approve requests** setting decides what happens next, with the daily ceiling, the pending cap and the already-in-the-library check all still applying; there is no way to approve on create. `forUser` takes the id from `GET /api/v1/auth/users`, not a username, so a rename does not move requests. `GET /api/v1/system/status` reports `"features": {"requestsForUser": true}` so an integration can tell a Bindery that accepts the field from an older one. The Requests page and the queue API record who actually asked: "made for them with the API key", or "made for them by" the admin whose session sent it. That is stored on the request itself, not in History, because History is the book owner's record and the request queue is admin information. No other route accepts `forUser`, and there is no general "act as this user" header.

**Requester restrictions hold in every auth mode.** `disabled` and `local-only` mode serve an anonymous caller (or, in `local-only`, any client on a private network) as the admin, but a request carrying a requester's own session is never elevated that way: it acts as the requester, and so does its OPDS access. Two things still act as the admin whatever cookie rides along: the API key, and, in those two modes, a caller who simply signs out, because the mode itself admits them. So a requester account only means something when the person cannot reach Bindery without signing in, which in practice is `enabled` or `proxy` mode.

New requests notify the **Request** webhook at most once an hour for the same item from the same person, so withdrawing and asking again does not repeat the alert, and at most 10 times an hour per requester; past that the requests are still stored and shown under Requests, only the webhook is skipped. Titles, authors and usernames in that webhook have mentions, Slack escapes, markdown links and link schemes neutralised, so nothing in them is clickable or pings a channel.

**Tenancy.** With `BINDERY_ENFORCE_TENANCY` off (the default), a requester's Library lists every book in the instance, whoever added it, and approved requests are simply part of the shared library. With it on, the Library lists the requester's own books plus unowned ones, which after an approval means what they asked for. Either way a requester only ever sees their own requests, and a request for a book another user already has is refused as already in the library, because a book can only be in the library once. A request for an author is refused only when the requester can already see that author (with tenancy off, when anyone has it); with tenancy on, another user having the author does not stop the request, and approving it gives the requester their own copy of the author.

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

A user who owns nothing is deleted straight away. A user who owns library data is not: the request answers `409` with a `counts` object saying how much of each kind they own, and nothing is changed. The Users page does the same through a dialog that shows what the account owns and asks whether to keep the library (hand it to another user, or leave it with no owner so everyone sees it) or delete it too. Through the API, repeat the request with a `strategy`:

```bash
# Move it to another account
curl -X DELETE "http://bindery:8787/api/v1/auth/users/2?strategy=reassign&reassignTo=3" \
  -H "X-Api-Key: <admin-api-key>"

# Delete it
curl -X DELETE "http://bindery:8787/api/v1/auth/users/2?strategy=purge" \
  -H "X-Api-Key: <admin-api-key>"
```

- **`reassign`** hands every row the account owns, plus its history, to the account in `reassignTo`. Leave `reassignTo` out and the rows lose their owner, which makes them shared (with tenancy on, every user can see them and only an admin can change them). `reassignTo` must be an existing account other than the one being deleted.
- **`purge`** deletes the downloads, books, authors, import lists and history the account owns, along with any other rows recorded against it. Books that other accounts (or nobody) own are kept, even when they sit under one of the account's authors. That author stays: it goes to the account that owns the remaining books, or becomes shared when they have several owners or one has no owner. The `counts` object reports these as `othersBooks` and `othersBookFiles`; they do not trigger the `409`, and the dialog tells you how many books are kept.

Under either strategy:

- The user's requests are deleted, even with `reassign`.
- Recommendations, dismissals and the user's Refresh all status are removed.
- Blocklist entries stay, with the record of who added them cleared. They are shown in the `counts` but do not trigger the `409`, so an account whose only data is blocklist entries is deleted at once.
- The last admin cannot be deleted: the request is refused with `400` and `cannot delete the last admin user`. If that admin owns data and you gave no strategy, the `409` comes first.
- You cannot delete the account you are signed in with: the request is refused with `409` before anything else is checked, whatever the `strategy`, and nothing is changed. Sign in as another admin to remove it. The exemption follows the credential: a request made with the API key, or let in as admin by the `disabled` or `local-only` auth mode, acts as the install rather than as a person, so it can still delete any account the last admin rule allows. That includes a signed in session that also sends a valid API key. The Users page disables Delete on your own row, and on the last admin's.

An unknown `strategy` or a `reassignTo` that is not a number answers `400`. `reassignTo` is ignored under `purge`. If the delete still finds rows pointing at the user, it answers `409` with `rows still reference this user` and nothing is deleted; that means a bug, so report it.

## Settings UI layout

Everyone sees the **General** tab (appearance, and the Security section, which is where a user changes their own password) and **About**. The API key, the session secret rotation, downloads, file naming, storage and backup render inside General for admins only. The remaining tabs are admin-only, in four groups:

- **Sources** — Indexers, Download Clients
- **Library** — Quality Profiles, Metadata Profiles, Root Folders
- **Integrations** — Notifications, Calibre, Audiobookshelf, Grimmory, API Keys
- **System** — Import / Migrate, Blocklist, Logs

Non-admins who open an admin tab are redirected back to General; admin API routes return 403. Inside General itself, a non admin sees Appearance and Security; the sections that describe or configure the server (file naming, downloads, search, the default library location, storage, the library scan panel and the schedule intervals) render for admins only, and the routes behind them, including `GET /system/storage` and `GET /library/scan/status`, answer 403 to anyone else. The settings API follows the same line: a non admin reading `GET /api/v1/setting` or `GET /api/v1/setting/{key}` gets only the handful of settings a user screen needs (whether Discover recommendations are on, the primary metadata provider, and the add author defaults: default root folder ids, default format and monitoring), and every other key is left out of the list or answers 404 as if it were unset. Filesystem paths, integration URLs and usernames, the telemetry install id and every operator knob stay with admins, as does `GET /grimmory/config`. Requests made with the API key count as admin and see every non secret setting. In the `disabled` and `local-only` auth modes every admitted request counts as admin, so this filter applies only once logins are enforced. Users are managed on the dedicated **Users** page (the people icon in the header), not inside Settings.

## CSRF tokens

Session cookie mutations pass two guards: the `X-Requested-With: bindery-ui` header, and a double submit `X-CSRF-Token`. Both must be present. v1.0 added the token alongside the header check rather than replacing it.

**Browser users:** the UI handles this transparently.

**API scripts using session cookies:** fetch a token first:

```bash
TOKEN=$(curl -s -b "bindery_session=<value>" \
  http://bindery:8787/api/v1/auth/csrf | jq -r .csrfToken)

curl -X POST http://bindery:8787/api/v1/author \
  -b "bindery_session=<value>" \
  -H "X-Requested-With: bindery-ui" \
  -H "X-CSRF-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"foreignAuthorId": "<id from /search/author>", "authorName": "Ursula K. Le Guin"}'
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
| `DELETE /auth/users/{id}` answers 409 with a `counts` object | The user owns library data, and Bindery will not guess whether to keep or delete it | Repeat the request with `?strategy=reassign` (optionally `&reassignTo=<id>`) to move the data, or `?strategy=purge` to delete it (see "Deleting a user" above). |
| `403 Forbidden` on an API call that worked before v1.0 | Session-cookie mutations now require `X-CSRF-Token` | Switch callers to `X-Api-Key` auth (CSRF-exempt), or add a `GET /auth/csrf` preflight to your script (see "CSRF tokens" above). |
| Admin locked out: no admin account exists or all admins deleted | User row has `role='user'` or all admin rows were removed | Recover via direct DB update: `sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE username='<your-username>';"`. The Bindery image has no shell or `sqlite3`, so `kubectl exec` / `docker exec` cannot run it. On Docker or a bare install, run it from the host; no restart is needed there. On Kubernetes, scale Bindery to zero first (the PVC is ReadWriteOnce), run it from a helper pod that has `sqlite3` and mounts `bindery-config` (for example `alpine`, then `apk add sqlite`; busybox has no `sqlite3`), then scale back up. |
| OIDC user auto-provisioned as `user` but should be `admin` | `BINDERY_OIDC_ADMIN_GROUP` not set, so the IdP group is never consulted | Set `BINDERY_OIDC_ADMIN_GROUP` to the IdP group name (and `BINDERY_OIDC_GROUP_CLAIM` if your IdP emits groups under a non-default claim). To promote a single existing account without group mapping: `PUT /api/v1/auth/users/{id}/role` with `{"role": "admin"}`. |
| Non-admin user can reach admin settings page in UI | Browser cached a pre-v1.0 session or route bundle | Hard-refresh the page (`Ctrl+Shift+R`). If it persists, log out and back in. The backend enforces role checks regardless of what the UI renders. |
