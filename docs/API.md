# API

Bindery exposes a REST API at `/api/v1/*`. Every Bindery feature is reachable from the API — the React UI uses the same endpoints. There is also a small `/api/queue` surface that mimics the Sonarr/Radarr queue contract for external tooling.

> The handler list below is a representative selection. The router lives in [`cmd/bindery/main.go`](../cmd/bindery/main.go) and registers ~180 endpoints; that file is the source of truth.

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
GET    /api/v1/author/{id}/relink-upstream/candidates
                                                    search metadata candidates for manual relink
POST   /api/v1/author/{id}/relink-upstream        re-bind to a different foreign ID
GET    /api/v1/author/{id}/aliases                list merged-in alias rows
POST   /api/v1/author/{id}/merge                  merge another author into this one
```

`POST /api/v1/author/{id}/relink-upstream` may be called without a body for
automatic upstream matching. Manual relink can send:

```json
{
  "foreignAuthorId": "hc:example-or-dnb:123",
  "authorName": "Selected Candidate Name"
}
```

### Books

```
GET    /api/v1/book?status=wanted                 filter by status (wanted, downloaded, …)
POST   /api/v1/book/bulk                          bulk monitor / status flip
GET    /api/v1/book/{id}                          book detail (with editions, history, formats)
PUT    /api/v1/book/{id}                          update monitor / status / metadata
DELETE /api/v1/book/{id}                          remove from library
DELETE /api/v1/book/{id}/file                     delete imported file(s) on disk
PUT    /api/v1/book/{id}/exclude                  exclude from future searches
POST   /api/v1/book/{id}/rebind                   re-link to a different metadata record
POST   /api/v1/book/{id}/enrich-audiobook         pull narrator/duration/cover from Audnex
POST   /api/v1/book/{id}/search                   manual indexer search
GET    /api/v1/book/{id}/file                     download the imported file (auth required)
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

### Download clients, queue, history, blocklist

```
GET    /api/v1/downloadclient                     list (filtered by visibility)
POST   /api/v1/downloadclient                     add (admin)
POST   /api/v1/downloadclient/{id}/test           probe connectivity (admin)

GET    /api/v1/queue                              active downloads with live downloader overlay
POST   /api/v1/queue/grab                         submit a search result to the download client
POST   /api/v1/queue/{id}/retry-import           retry an importFailed item without re-downloading
DELETE /api/v1/queue/{id}                         remove (also from downloader)

GET    /api/v1/pending                            grabs awaiting delay-profile clearance
POST   /api/v1/pending/{id}/grab                  promote pending to queue immediately

GET    /api/v1/history                            grab / import / failure timeline
POST   /api/v1/history/{id}/blocklist             add the release to the blocklist

GET    /api/v1/blocklist                          list blocked releases
DELETE /api/v1/blocklist/{id}                     remove an entry
DELETE /api/v1/blocklist/bulk                     bulk remove
```

### Notifications, backups, system

```
GET    /api/v1/notification                       list webhooks
POST   /api/v1/notification                       create
POST   /api/v1/notification/{id}/test             fire a test event

POST   /api/v1/backup                             snapshot the SQLite database
GET    /api/v1/system/status                      version, uptime, build info
PUT    /api/v1/system/loglevel                    runtime log-level switch (debug/info/warn/error)
GET    /api/v1/images?url=<encoded>               proxied + cached cover image (30-day TTL)
```

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

Bindery serves an OPDS 1.2 catalogue at `/opds/v1.2/`:

- `/opds/v1.2/` — catalog root
- `/opds/v1.2/recent` — recently imported
- `/opds/v1.2/authors` and `/opds/v1.2/authors/{id}` — by author
- `/opds/v1.2/search?q=...` — search

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

**Fire a test webhook:**

```bash
curl -X POST -H "X-Api-Key: $KEY" \
  http://bindery:8787/api/v1/notification/1/test
```

## URL base (reverse-proxy subpath)

When Bindery is mounted under a path prefix (e.g. `https://example.com/bindery`), set `BINDERY_URL_BASE=/bindery`. All route prefixes — including `/api/v1`, `/api/queue`, and `/opds/v1.2` — are served under that base, and the embedded React SPA emits matching URLs. See [DEPLOYMENT.md](DEPLOYMENT.md#environment-variables) for full details.
