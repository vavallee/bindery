# API

Bindery exposes a REST API at `/api/v1/*`. Every Bindery feature is reachable from the API — the React UI uses the same endpoints. There is also a small `/api/queue` surface that mimics the Sonarr/Radarr queue contract for external tooling.

> The handler list below is a representative selection. The router lives in [`cmd/bindery/main.go`](../cmd/bindery/main.go) and registers over 100 endpoints; that file is the source of truth.

## Authentication

Every request to `/api/v1/*` is authenticated **except** the bootstrap and identity endpoints:

- `GET  /api/v1/health`
- `GET  /api/v1/auth/status`
- `POST /api/v1/auth/login`, `/auth/logout`, `/auth/setup`
- `GET  /api/v1/auth/oidc/{provider}/login` and `/callback`

A request is allowed if **any** of the following holds:

1. Auth mode is **Disabled** (configured in Settings → General → Security).
2. Auth mode is **Local only** and the request originates from a private-range IP — `10/8`, `172.16/12`, `192.168/16`, `127/8`, IPv6 ULA, link-local, loopback.
3. The request carries a valid `X-Api-Key` header (or `?apikey=` query parameter) matching the stored key.
4. The request carries a valid `bindery_session` cookie.
5. Auth mode is **Proxy** and a trusted upstream forwards `X-Forwarded-User` matching a Bindery account (see [auth-proxy.md](auth-proxy.md)).

Otherwise the server returns `401`. Browser sessions also need a CSRF double-submit token on mutating requests (`POST` / `PUT` / `DELETE`); API-key clients are exempt from CSRF.

Non-browser clients (curl, scripts, mobile apps) authenticating via API key do **not** need to send an `X-Requested-With: bindery-ui` header — that header is required only for browser sessions to satisfy the CSRF gate. The auth endpoints listed above (`/auth/login`, `/auth/logout`, `/auth/setup`, `/auth/status`, `/auth/csrf`) are exempt from the `X-Requested-With` check entirely, since there is no session to protect at that stage.

The API key lives in **Settings → General → Security**. Regenerating it invalidates every existing consumer.

## Endpoint catalogue (selection)

### Authors

```
GET    /api/v1/author                             list authors (paginated, filterable)
POST   /api/v1/author                             add an author (triggers async book fetch)
POST   /api/v1/author/bulk                        bulk add/update
GET    /api/v1/author/{id}                        author detail
PUT    /api/v1/author/{id}                        update monitored / metadata profile
DELETE /api/v1/author/{id}                        remove (with optional file delete)
POST   /api/v1/author/{id}/refresh                re-pull works from OpenLibrary
GET    /api/v1/author/{id}/catalogue-reconciliation
                                                    preview stale metadata-only Wanted rows
POST   /api/v1/author/{id}/catalogue-reconciliation
                                                    recheck and remove selected preview rows
GET    /api/v1/author/{id}/relink-upstream/candidates
                                                    search metadata candidates for manual relink
POST   /api/v1/author/{id}/relink-upstream        re-bind to a different foreign ID
GET    /api/v1/author/{id}/aliases                list merged-in alias rows
POST   /api/v1/author/{id}/merge                  merge another author into this one
```

`GET /api/v1/author/{id}` includes a `lastSync` object when this Bindery process
has synced the author's catalogue since it started — what the provider returned
and what each filter dropped, so a catalogue that was filtered down is
distinguishable from an author who wrote that few books:

```json
{
  "lastSync": {
    "completedAt": "2026-08-11T12:00:00Z",
    "total": 66,
    "added": 1,
    "skippedLanguage": 65,
    "skippedJunk": 0,
    "skippedMediaType": 0,
    "allowedLanguages": ["eng"],
    "unknownLanguageFail": true,
    "skippedLanguageSample": [{ "title": "Les Ours", "language": "fre" }]
  }
}
```

It is held in memory, not stored, so it is absent after a restart until the
author is synced again. `skippedLanguageSample` is capped at a few titles; the
counts are exact.

`POST /api/v1/author` responses include a `providerMismatch` object when the
linked record routes its catalogue syncs to a provider other than the
configured `metadata.primary_provider` (#2237), so a fallback pick is visible
instead of silent:

```json
{
  "providerMismatch": {
    "primaryProvider": "hardcover",
    "linkedProvider": "openlibrary"
  }
}
```

The field is absent when the providers match or no primary is configured.

Catalogue reconciliation is deliberately separate from refresh. The GET route
queries the current primary provider without using its cached author catalogue
and returns `candidates`, a reason-count summary, protection counts, and
`providerComplete`. A partial provider result never treats absence as a reason
to remove a row. The POST route accepts the IDs from the preview:

```json
{ "bookIds": [12, 19] }
```

The server recomputes the preview and deletes only IDs that are still
candidates and still match the database-level guard: same author, status
`wanted`, not excluded, no legacy file-path columns, and no `book_files` rows.
It returns an `applied` summary with `requested`, `deleted`, and `skipped`.
Neither route removes files from disk.

`POST /api/v1/author/{id}/relink-upstream` may be called without a body for
automatic upstream matching. Manual relink can send:

```json
{
  "foreignAuthorId": "hc:example-or-dnb:123",
  "authorName": "Selected Candidate Name"
}
```

The automatic form answers **503** when the provider configured as
`metadata.primary_provider` did not respond and the only candidates came from a
fallback provider. Nothing is written in that case. The foreign ID a relink
stores is what every later catalogue sync reads the provider back off, so a
link written while the primary was rate limited would silently and permanently
move that author onto the fallback (#2271). Retry once the primary is
answering, or send an explicit `foreignAuthorId`, which skips the search
entirely.

### Books

```
GET    /api/v1/book?status=wanted                 filter by status (wanted, downloaded, …)
POST   /api/v1/book/bulk                          bulk monitor / status flip
GET    /api/v1/book/{id}                          book detail (with editions, history, formats)
PUT    /api/v1/book/{id}                          update monitor / status / metadata
DELETE /api/v1/book/{id}                          remove from library
DELETE /api/v1/book/{id}/file                     delete imported file(s) on disk (`?format=ebook|audiobook` scopes to one format; `?path=…` deregisters one tracked path WITHOUT deleting anything on disk)
PUT    /api/v1/book/{id}/exclude                  exclude from future searches
POST   /api/v1/book/{id}/rebind                   re-link to a different metadata record
POST   /api/v1/book/{id}/enrich-audiobook         pull narrator/duration/cover from Audnex
POST   /api/v1/book/{id}/search                   manual indexer search
GET    /api/v1/book/{id}/file                     download the imported file (auth required; `?format=ebook|audiobook` picks the format on dual-format books)
```

### Search & discovery

```
GET    /api/v1/search/author?q=…                  OpenLibrary author search
GET    /api/v1/search/book?q=…                    OpenLibrary book search
GET    /api/v1/book/lookup?isbn=…                 ISBN-keyed lookup
GET    /api/v1/wanted/missing                     list wanted-but-missing books
POST   /api/v1/wanted/bulk                        bulk operations on wanted
```

### Indexers, Prowlarr, root folders

```
GET    /api/v1/indexer                            list configured indexers
POST   /api/v1/indexer                            add (admin)
PUT    /api/v1/indexer/{id}                       update (admin)
DELETE /api/v1/indexer/{id}                       remove (admin)
POST   /api/v1/indexer/{id}/test                  probe connectivity
GET    /api/v1/indexer/search?q=…                 multi-indexer ad-hoc query
GET    /api/v1/search/last-debug                  last query plan & raw responses (debugging)

GET    /api/v1/prowlarr                           list registered Prowlarr servers
POST   /api/v1/prowlarr                           add a Prowlarr server
POST   /api/v1/prowlarr/{id}/sync                 import indexers from Prowlarr

GET    /api/v1/rootfolder                         list library roots
POST   /api/v1/rootfolder                         add a new root
DELETE /api/v1/rootfolder/{id}                    remove
```

#### Indexer and Prowlarr API keys are write-only

Indexer and Prowlarr responses never carry the stored credential. Every
endpoint that returns one of these objects sends `apiKey` as an empty string
and adds a read-only `apiKeyConfigured` boolean saying whether a key is stored:

```json
{
  "id": 3,
  "name": "NZBGeek",
  "url": "https://api.nzbgeek.info",
  "apiKey": "",
  "apiKeyConfigured": true
}
```

`apiKeyConfigured` is response only. It is not persisted, and sending it is
ignored.

On `PUT /api/v1/indexer/{id}` and `PUT /api/v1/prowlarr/{id}` the key follows
the same rules the import-list endpoints already use:

| Request body | Result |
| --- | --- |
| `apiKey` omitted | the stored key is kept |
| `"apiKey": ""` | the stored key is kept |
| `"apiKey": "newkey"` | the stored key is replaced |
| `"clearApiKey": true` | the stored key is removed |
| `"apiKey": "newkey"` with `"clearApiKey": true` | `400`, nothing is changed |

Keeping the stored key on a blank value is what lets a client read an object,
edit an unrelated field, and send it straight back without wiping the
credential. It also means a blank submit on a Prowlarr instance no longer
cascades an empty key to every indexer synced from it. Removing a key is
therefore always deliberate: it takes `clearApiKey`.

Setting a key still works normally on create, and `POST /api/v1/indexer/test`
still accepts an inline `apiKey` so an unsaved configuration can be probed.
To test a saved indexer against its stored key, use
`POST /api/v1/indexer/{id}/test`.

### Download clients, queue, history, blocklist

```
GET    /api/v1/downloadclient                     list (filtered by visibility)
POST   /api/v1/downloadclient                     add (admin)
GET    /api/v1/downloadclient/{id}                fetch one (admin)
PUT    /api/v1/downloadclient/{id}                update (admin)
DELETE /api/v1/downloadclient/{id}                remove (admin)
POST   /api/v1/downloadclient/{id}/test           probe connectivity (admin)
POST   /api/v1/downloadclient/test                probe an unsaved config (admin)

GET    /api/v1/queue                              active downloads with live downloader overlay
POST   /api/v1/queue/grab                         submit a search result to the download client
POST   /api/v1/queue/{id}/retry-import           retry an importFailed item without re-downloading
DELETE /api/v1/queue/{id}                         remove (also from the download client)
       ?deleteFiles=true                          have the client destroy the data too
       ?removeFromClient=false                    forget Bindery's row only, leave the torrent/NZB in the client
POST   /api/v1/queue/bulk-delete                  remove many; {"ids":[..],"deleteFiles":false,"unmonitorBooks":false,"removeFromClient":true}

GET    /api/v1/pending                            grabs awaiting delay-profile clearance
POST   /api/v1/pending/{id}/grab                  promote pending to queue immediately

GET    /api/v1/queue/manual-import/lookup         parse + catalogue-match one path (admin)
GET    /api/v1/queue/manual-import/scan           enumerate + match book units under a folder (admin)
POST   /api/v1/queue/manual-import                import one path against a book (admin)
POST   /api/v1/queue/manual-import/batch          import selected {path, bookId} pairs (admin)
POST   /api/v1/queue/manual-import/reassign       move a mis-matched file to another book (admin)
GET    /api/v1/queue/manual-import/reassign/preview  where that reassign would move and rename it (admin)
                                                    ?path=…&targetBookId=N[&format=ebook|audiobook]
POST   /api/v1/queue/manual-import/match          attach an importFailed download to a book and import its files (admin)

GET    /api/v1/reorganize/preview                 preview renaming tracked files to the current template (admin)
                                                    ?scope=book|author|library (&id=N for book/author)
POST   /api/v1/reorganize/apply                   move the selected files {fileIds:[…]} to their templated paths (admin)

GET    /api/v1/history                            grab / import / failure timeline
POST   /api/v1/history/{id}/blocklist             add the release to the blocklist

GET    /api/v1/blocklist                          list blocked releases
DELETE /api/v1/blocklist/{id}                     remove an entry
DELETE /api/v1/blocklist/bulk                     bulk remove
```

#### Download client credentials are write-only

Every download client response blanks `apiKey` and `password` and reports
whether one is stored through two extra booleans, so a stored secret is never
handed back to a caller:

```json
{
  "id": 3,
  "name": "qBittorrent",
  "type": "qbittorrent",
  "username": "admin",
  "apiKey": "",
  "password": "",
  "apiKeyConfigured": false,
  "passwordConfigured": true
}
```

`PUT /api/v1/downloadclient/{id}` decodes over the stored row, so any key you
leave out keeps its stored value rather than being reset. That applies to the
credentials too:

* `apiKey` absent, or `""`: keeps the stored API key.
* `apiKey: "new-value"`: replaces the stored API key.
* `clearApiKey: true`: removes the stored API key.
* `apiKey: "new-value"` together with `clearApiKey: true`: rejected with `400`, because the two contradict each other.

`password` and `clearPassword` behave the same way. Because an empty string now
means "keep", moving a client between a password type and an API-key type has
to send the clear flag for the credential it is abandoning, otherwise the old
secret stays on the row.

Booleans follow the same rule: omitting `enabled` or `useSsl` leaves them as
they were, and an explicitly sent `false` still turns them off.

`POST /api/v1/downloadclient/test` probes a config without saving it. Include
the saved client's `id` and the handler fills in a credential you left blank
from that row, but only while `type`, `host`, `port`, `useSsl` and `urlBase`
still match it. Point the probe anywhere else and it runs with whatever
credential the body carried.

### Notifications, backups, system

```
GET    /api/v1/notification                       list webhooks
POST   /api/v1/notification                       create
POST   /api/v1/notification/{id}/test             fire a test event

POST   /api/v1/backup                             snapshot the SQLite database (optional {"label": "..."})
GET    /api/v1/backup                             list stored backups
DELETE /api/v1/backup/{filename}                  delete one backup
POST   /api/v1/backup/{filename}/restore          stage a backup for the next restart (admin, X-Confirm-Restore: true)
GET    /api/v1/system/status                      version, uptime, build info
PUT    /api/v1/system/loglevel                    runtime log-level switch (debug/info/warn/error)
GET    /api/v1/images?url=<encoded>               proxied + cached cover image (30-day TTL)
```

#### Webhook payload

Every event POSTs a JSON body with a consistent shape so relays render it
without a custom template:

| Field | Meaning |
|-------|---------|
| `eventType` | `grabbed` \| `bookImported` \| `upgrade` \| `downloadFailed` \| `health` \| `test` — present on **every** event |
| `title` | what happened, e.g. `Release Grabbed`, `Book Imported`, `Download Failed` |
| `message` | the subject, e.g. `The Way of Kings · Brandon Sanderson` |
| `body` | alias of `message` (Apprise requires a `body` field) |
| `item` | the raw release/book name (the title before it was moved into `message`) |
| `format` | `ebook` \| `audiobook` on `bookImported` and `upgrade`. **Omitted for Apprise targets only** (a URL with a `/notify` path segment) — Apprise reserves `format` for the body markup and rejects anything but `text`/`html`/`markdown` with HTTP 400. Every other consumer still receives it |
| `mediaFormat` | the same value as `format`, always present. Use this one if your relay is Apprise, or if you want a key that is never stripped |
| event extras | `author`, `size`, `path`, `status`, `clientId` when relevant |

**ntfy:** set the notification's **topic** field and point the URL at the ntfy
server root (e.g. `https://ntfy.sh`). Bindery then POSTs the JSON body with a
`topic` field to the root, which ntfy renders natively. Without a topic it POSTs
to the URL as-is, so a topic URL would show the raw JSON — use the topic field
or ntfy message-templating headers (`X-Title`, `X-Message`) instead.

### Auth and users (admin)

```
GET    /api/v1/auth/status                        public — am I logged in?
GET    /api/v1/auth/csrf                          fetch a CSRF token for browser flows
POST   /api/v1/auth/login                         username + password
POST   /api/v1/auth/logout
POST   /api/v1/auth/setup                         first-run admin creation (one-shot)
PUT    /api/v1/auth/mode                          switch enabled/local-only/disabled/proxy (admin)
POST   /api/v1/auth/password                      change own password
POST   /api/v1/auth/apikey/regenerate             rotate the API key

GET    /api/v1/auth/oidc/providers                list configured providers
PUT    /api/v1/auth/oidc/providers                update providers (admin)
GET    /api/v1/auth/oidc/{provider}/login         start an OIDC login
GET    /api/v1/auth/oidc/{provider}/callback      OIDC redirect target

GET    /api/v1/auth/users                         list users (admin)
POST   /api/v1/auth/users                         create (admin)
DELETE /api/v1/auth/users/{id}                    delete (admin)
PUT    /api/v1/auth/users/{id}/role               change role (admin)
PUT    /api/v1/auth/users/{id}/reset-password     reset (admin)
```

### Arr-compatible queue

```
GET    /api/queue                                 Sonarr/Radarr-style queue payload
```

This endpoint sits **outside** `/api/v1/` and matches the queue contract used by [Harpoon](https://github.com/harpoon-io/harpoon) and similar *arr-aware tools. It returns `totalRecords`, supports pagination and sort, and surfaces per-record `size`, `sizeleft`, `status`, `client`, `remote ID`, and `protocol`. API-key authentication is required; browser-session CSRF protections do not apply.

## OPDS

Bindery serves an OPDS 1.2 catalogue at `/opds/`:

- `/opds/` — catalog root
- `/opds/recent` — recently imported
- `/opds/authors` and `/opds/authors/{id}` — by author
- `/opds/series` and `/opds/series/{id}` — by series
- `/opds/book/{id}` — book entry
- `/opds/book/{id}/file` — download the book file
- `/opds/images?url=<encoded>` — cover images, cached locally

Cover links in the feed point at `/opds/images` rather than at the metadata
provider, so reading apps fetch every cover from your instance. It is the same
handler and the same `<dataDir>/image-cache/` the web UI uses via
`/api/v1/images`, mounted a second time inside `/opds` because that route
requires a session cookie or the API key and reading apps authenticate with
HTTP Basic. See [third-party-data.md](third-party-data.md) for why covers are
served this way.

OPDS authenticates via HTTP Basic — any username, API key as the password. KOReader, Moon+ Reader, Aldiko, and other OPDS-capable apps work out of the box.

## Examples

**Add an author by OpenLibrary ID:**

```bash
curl -X POST -H "X-Api-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"foreignAuthorId":"OL23919A","monitored":true,"searchOnAdd":true}' \
  http://bindery:8787/api/v1/author
```

**List wanted books for a specific author:**

```bash
curl -H "X-Api-Key: $KEY" \
  "http://bindery:8787/api/v1/book?status=wanted&authorId=42"
```

**Trigger a manual search and inspect what the indexer returned:**

```bash
curl -X POST -H "X-Api-Key: $KEY" http://bindery:8787/api/v1/book/123/search
curl -H "X-Api-Key: $KEY" http://bindery:8787/api/v1/search/last-debug
```

**Snapshot the database before an upgrade:**

```bash
curl -X POST -H "X-Api-Key: $KEY" http://bindery:8787/api/v1/backup
```

The body is optional. Pass a `label` to make the snapshot identifiable — it is
appended to the filename as `bindery_<timestamp>_<label>.db`:

```bash
curl -X POST -H "X-Api-Key: $KEY" -H 'Content-Type: application/json' \
  -d '{"label":"pre-upgrade"}' http://bindery:8787/api/v1/backup
```

The label is sanitised server-side before it reaches the filename: only
`A-Za-z0-9_-` survive, every other character (including `/`, `\`, `.` and any
non-ASCII) collapses to `-`, and the result is capped at 40 characters. A label
that sanitises to nothing — an all-CJK label, for example — is dropped and the
snapshot keeps the plain timestamp name.

**Restore a snapshot:**

```bash
curl -X POST -H "X-Api-Key: $KEY" -H 'X-Confirm-Restore: true' \
  http://bindery:8787/api/v1/backup/bindery_20260101_120000_pre-upgrade.db/restore
```

Admin only, and the `X-Confirm-Restore: true` header is required. A `200` means
the backup passed its integrity check and is staged, not that it is live:
Bindery runs SQLite in WAL mode, so writing the running database out from under
its own write-ahead log would replay stale pages over the restored ones.
The file is copied to `<database>.restore-pending` and swapped in on the **next
start**, so restart Bindery to finish the restore. A backup that is not a sound
SQLite database is rejected with `400` and nothing is staged.

**Fire a test webhook:**

```bash
curl -X POST -H "X-Api-Key: $KEY" \
  http://bindery:8787/api/v1/notification/1/test
```

## URL base (reverse-proxy subpath)

When Bindery is mounted under a path prefix (e.g. `https://example.com/bindery`), set `BINDERY_URL_BASE=/bindery`. All route prefixes — including `/api/v1`, `/api/queue`, and `/opds` — are served under that base, and the embedded React SPA emits matching URLs. See [DEPLOYMENT.md](DEPLOYMENT.md#environment-variables) for full details.
