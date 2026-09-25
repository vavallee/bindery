# API

Bindery exposes a REST API at `/api/v1/*`. Every Bindery feature is reachable from the API — the React UI uses the same endpoints. There is also a small `/api/queue` surface that mimics the Sonarr/Radarr queue contract for external tooling.

> The handler list below is a representative selection. The router lives in [`cmd/bindery/main.go`](../cmd/bindery/main.go) and registers over 100 endpoints; that file is the source of truth.

## Authentication

Every request to `/api/v1/*` is authenticated **except** the bootstrap and identity endpoints:

- `GET  /api/v1/health`
- `GET  /api/v1/auth/status`
- `POST /api/v1/auth/login`, `/auth/logout`, `/auth/setup`
- `GET  /api/v1/auth/oidc/{provider}/login` and `/callback`
- `GET  /api/v1/auth/csrf`
- `GET  /api/v1/auth/oidc/providers` (the read path only; `PUT` on the same path is an admin mutation)

A request is allowed if **any** of the following holds:

1. Auth mode is **Disabled** (configured in Settings → General → Security).
2. Auth mode is **Local only** and the request originates from a private-range IP — `10/8`, `172.16/12`, `192.168/16`, `127/8`, IPv6 ULA, link-local, loopback.
3. The request carries a valid `X-Api-Key` header matching the stored key. The `?apikey=` query parameter is accepted too, but only on `GET`, `HEAD` and `OPTIONS`: a key in a URL leaks into proxy logs, browser history and `Referer`, so it cannot authorise a mutation. Mutations must send the header.
4. The request carries a valid `bindery_session` cookie.
5. Auth mode is **Proxy** and a trusted upstream forwards `X-Forwarded-User` matching a Bindery account (see [auth-proxy.md](auth-proxy.md)).

Otherwise the server returns `401`. Browser sessions also need a CSRF double-submit token on mutating requests (anything other than `GET`, `HEAD` and `OPTIONS`); API-key clients are exempt from CSRF.

Non-browser clients (curl, scripts, mobile apps) authenticating via API key do **not** need to send an `X-Requested-With: bindery-ui` header — that header is required only for browser sessions to satisfy the CSRF gate. The auth endpoints listed above (`/auth/login`, `/auth/logout`, `/auth/setup`, `/auth/status`, `/auth/csrf`) are exempt from the `X-Requested-With` check entirely, since there is no session to protect at that stage.

The API key lives in **Settings → General → Security**. Regenerating it invalidates every existing consumer.

## Endpoint catalogue (selection)

### Authors

```
GET    /api/v1/author                             list authors (paginated, filterable)
POST   /api/v1/author                             add an author (triggers async book fetch)
POST   /api/v1/author/book                        add one book by foreign id (creates its author if needed)
POST   /api/v1/author/bulk                        bulk add/update
GET    /api/v1/author/{id}                        author detail
PUT    /api/v1/author/{id}                        update monitored / metadata profile
DELETE /api/v1/author/{id}                        remove (with optional file delete)
POST   /api/v1/author/{id}/refresh                re-pull profile and works, skipping the metadata cache; 409 while a sync for that author is running
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

`POST /api/v1/author/book` answers **409 Conflict** when `foreignBookId` (or
the canonical id it resolves to on the ISBN path) already matches a book in the
requesting user's library (#1227). "In the library" is scoped exactly like
`GET /api/v1/book`: rows the user owns plus rows with no owner, and every row
for an admin or when tenancy is off. The check runs before any author creation
or provider fetch, so a conflict has no side effects, and the existing row is
left exactly as it was: an unmonitored book stays unmonitored. The body mirrors
the Add Author conflict:

```json
{
  "error": "book already in your library; change its format or monitoring from the book page",
  "existingBookId": 42,
  "existingBook": { "id": 42, "foreignBookId": "OL27448W", "title": "The Hobbit", "monitored": false }
}
```

To change the format or monitoring of a book you already have (for example an
imported ebook you now also want as an audiobook), update it through
`PUT /api/v1/book/{id}` or the book page rather than re-adding it.

With tenancy enforced, a non admin user whose request reaches a copy of the
same foreign id held by a different user also gets **409**, with no row in the
body because the caller may not see it:

```json
{ "error": "book is held by another user" }
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

The same response carries `"syncInProgress": true` while a catalogue sync for
the author is running in this process, and omits it otherwise. The author page
polls it after **Refresh metadata** to show the result once the sync is done,
and `POST /api/v1/author/{id}/refresh` answers **409 Conflict** while it is set
instead of starting a second sync (#2601).

`POST /api/v1/author` accepts an optional `monitorNewItems` (#2541), the same
field `PUT /api/v1/author/{id}` takes: `all` (the default) lets a refresh add
newly discovered books, `none` keeps the catalogue as it was added. Any other
value is rejected with 400.

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

`GET /api/v1/author/{id}/relink-upstream/candidates` returns author records
from every configured provider, minus the one the author is linked to right now.
Records the author was linked to previously are returned with
`"previouslyLinked": true` rather than being hidden, so a relink can be undone
(#2688). The field is omitted on every other candidate.

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
GET    /api/v1/book?status=wanted                 filter by status (wanted, imported, skipped)
POST   /api/v1/book/bulk                          bulk monitor / status flip
GET    /api/v1/book/{id}                          book detail (with editions, history, formats)
PUT    /api/v1/book/{id}                          update monitor / status / metadata
DELETE /api/v1/book/{id}                          remove from library
DELETE /api/v1/book/{id}/file                     delete imported file(s) on disk (`?format=ebook|audiobook` scopes to one format; `?path=…` deregisters one tracked path WITHOUT deleting anything on disk)
PUT    /api/v1/book/{id}/exclude                  exclude from future searches
POST   /api/v1/book/{id}/rebind                   re-link to a different metadata record
POST   /api/v1/book/{id}/enrich-audiobook         pull narrator/duration/cover from Audnex
POST   /api/v1/book/{id}/search                   manual indexer search
GET    /api/v1/book/{id}/file                     download the imported file (auth required; `?path=…` serves one specific tracked file, for a book holding several of a format; `?format=ebook|audiobook` picks the format on dual-format books; `?path=` wins when both are sent)
```

### Series

```
GET    /api/v1/series                             list series with their linked books
GET    /api/v1/series/{id}                        one series
POST   /api/v1/series/{id}/fill                   add the series' missing books as wanted (admin)
PATCH  /api/v1/series/{id}                        monitor / unmonitor (admin)
```

`GET /series` returns the bare array it always has. Pagination is opt-in
(#2345): pass `limit` and/or `offset` and you get an `{items, total, limit,
offset}` envelope instead, which is what a large catalogue wants, since the
unpaged response carries every series with every linked book.

### Search & discovery

```
GET    /api/v1/search/author?term=…               metadata author search
GET    /api/v1/search/book?term=…                 metadata book search
GET    /api/v1/search/library?q=…                 your own catalogue: `{authors, books, series}`, up to `limit` rows per group (default 5, max 10), owner scoped like the list endpoints; 400 on an empty `q`
GET    /api/v1/book/lookup?isbn=… | ?asin=…       single-book lookup by identifier
GET    /api/v1/wanted/missing                     list wanted-but-missing books
POST   /api/v1/wanted/bulk                        bulk operations on wanted
```

Metadata results say when they are already in the caller's library (#1227).
Each `/search/book` and `/book/lookup` result carries `libraryBookId` when its
`foreignBookId` matches a book the requesting user owns, and each
`/search/author` result carries `libraryAuthorId` when its `foreignAuthorId`
matches a library author visible to the user, by primary id or by an alternate
identifier. Both fields hold the library row id and are omitted otherwise, so a
client can offer "open" for those rows and "add" for the rest. The lookup is
owner scoped: another user's copy does not stamp. If the library lookup itself
fails the search still succeeds, unstamped.

```json
[
  { "foreignBookId": "OL27448W", "title": "The Hobbit", "libraryBookId": 42 },
  { "foreignBookId": "OL27479W", "title": "The Silmarillion" }
]
```

### Indexers, Prowlarr, root folders

```
GET    /api/v1/indexer                            list configured indexers (admin)
GET    /api/v1/indexer/{id}                       fetch one (admin)
POST   /api/v1/indexer                            add (admin)
PUT    /api/v1/indexer/{id}                       update (admin)
DELETE /api/v1/indexer/{id}                       remove (admin)
POST   /api/v1/indexer/{id}/test                  probe a saved indexer (admin)
POST   /api/v1/indexer/test                       probe an unsaved config posted in the body (admin)
GET    /api/v1/indexer/search?q=…                 multi-indexer ad-hoc query
GET    /api/v1/search/last-debug                  last query plan & raw responses (debugging)

GET    /api/v1/prowlarr                           list registered Prowlarr servers (admin)
GET    /api/v1/prowlarr/{id}                      fetch one (admin)
POST   /api/v1/prowlarr                           add a Prowlarr server (admin)
PUT    /api/v1/prowlarr/{id}                      update (admin)
DELETE /api/v1/prowlarr/{id}                      remove (admin)
POST   /api/v1/prowlarr/{id}/test                 probe connectivity (admin)
POST   /api/v1/prowlarr/{id}/sync                 import indexers from Prowlarr (admin)

GET    /api/v1/rootfolder                         list library roots
POST   /api/v1/rootfolder                         add a new root (admin)
DELETE /api/v1/rootfolder/{id}                    remove (admin)
```

#### Per-indexer daily query cap

`dailyQueryLimit` on an indexer caps how many requests Bindery will send it in a
rolling 24 hours (#2312). Omitted, `null` and `0` all mean no cap. A negative
value is rejected with 400. `GET /indexer` and `GET /indexer/{id}` also return
`dailyQueriesUsed` on capped indexers, which is a display figure summed from the
stored hourly buckets and lags the live tally by up to one flush interval.

#### Rate limit holds

When Bindery is holding off on an indexer after a rate limit (#2640),
`GET /indexer` and `GET /indexer/{id}` carry `cooldownUntil` (a timestamp) and
`cooldownReason`. Both are response-only, are omitted when nothing is held, and
are ignored if a client sends them. The hold lives in memory, so it is gone
after a restart.

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
GET    /api/v1/downloadclient                     list (admin)
POST   /api/v1/downloadclient                     add (admin)
GET    /api/v1/downloadclient/{id}                fetch one (admin)
PUT    /api/v1/downloadclient/{id}                update (admin)
DELETE /api/v1/downloadclient/{id}                remove (admin)
POST   /api/v1/downloadclient/{id}/test           probe connectivity (admin)
POST   /api/v1/downloadclient/{id}/diagnose       path doctor: ordered checks with a fix each (admin)
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

`enabledForBooks` and `enabledForAudiobooks` gate which media type a client is
considered for at grab time, independent of `category`/`categoryAudiobook`.
Both default `true` on create when omitted, so a client handles every media
type until you opt it out of one. A grab is routed only to clients eligible
for its media type, tried in priority order with the next eligible client
retried if sending fails; if none are eligible, the grab fails with an error
naming the media type.

`POST /api/v1/downloadclient/test` probes a config without saving it. Include
the saved client's `id` and the handler fills in a credential you left blank
from that row, but only while `type`, `host`, `port`, `useSsl` and `urlBase`
still match it. Point the probe anywhere else and it runs with whatever
credential the body carried.

#### Diagnosing a download client

`POST /api/v1/downloadclient/{id}/diagnose` runs an ordered list of checks
against a saved client and answers `200` with the result. It takes the id
only, never a path, and it runs only when called.

```json
{
  "clientType": "qbittorrent",
  "checks": [
    {"code": "config", "status": "pass", "message": "The saved settings are usable."},
    {"code": "connect", "status": "pass", "message": "Connected to qBittorrent."},
    {"code": "category", "status": "pass", "message": "qBittorrent has the category \"books\"."},
    {"code": "client_path", "status": "pass", "message": "Completed downloads land in \"/torrents/books\", from the category save path."},
    {"code": "remap", "status": "pass", "message": "No path remap applies, so Bindery looks for \"/torrents/books\" at the same path."},
    {"code": "local_path", "status": "fail", "message": "Bindery would look for completed downloads in \"/torrents/books\", which is outside every folder it is configured to use (download folder \"/downloads\"). Bindery did not look inside it.", "fix": "Add a path remap on this client ..."},
    {"code": "hardlinks", "status": "skipped", "message": "Skipped because an earlier check failed."},
    {"code": "indexer_reach", "status": "unknown", "message": "Bindery cannot test whether the download client can reach your indexers, trackers or Usenet servers.", "fix": "..."}
  ],
  "paths": [{"clientPath": "/torrents/books", "source": "the category save path", "remapRule": "none", "localPath": "/torrents/books"}],
  "hardlinks": [],
  "primaryFix": "Add a path remap on this client ..."
}
```

* `code` is stable: `config`, `connect`, `category`, `client_path`, `remap`, `local_path`, `hardlinks`, `indexer_reach`. `message` and `fix` are English sentences.
* `status` is `pass`, `warn`, `fail`, `skipped` or `unknown`. Every check after a `fail` is `skipped` without running, except `indexer_reach`, which is always `unknown` because Bindery cannot test the client's own route to indexers.
* The folder checked is where a grab actually lands, worked out the way the grab itself is sent: the save path Bindery sends (rTorrent always, qBittorrent without a category, Transmission with an absolute category), the category save path (qBittorrent, with an empty one meaning the default save path plus the category name), the category folder (SABnzbd), the category DestDir or DestDir plus the category name when `AppendCategoryDir` is on (NZBGet), a Deluge label's move completed path, or the client default. `source` names which. The category is used exactly as a grab sends it: a category with spaces around it, or a Deluge category with capital letters (the Label plugin only accepts lowercase labels, so such grabs are not labelled), gives `client_path: warn`.
* Ebook and audiobook grabs are checked separately, because each resolves its own category and download folder. When both land in the same folder there is one `paths` row and the `client_path`, `remap` and `local_path` checks have no `mediaType`; otherwise each carries `mediaType` `ebook` or `audiobook`.
* `remapRule` is `client` (this client's path remap changed the path), `global` (`BINDERY_DOWNLOAD_PATH_REMAP` did) or `none`. A Windows drive path fails for a missing remap only when Bindery itself is not running on Windows. When no remap applies and the client's folder is not under any folder Bindery uses, `remap` is `warn` and `local_path` carries the failure and the fix. When Bindery sends the save path itself (rTorrent, qBittorrent without a category) and only the global remap is set, `remap` is `warn`: sending does not apply the global remap, so the client is given Bindery's own folder path and the round trip proves nothing.
* `hardlinks` has one row per download folder and library root pair, `{mediaType, downloadPath, root, result, linkable, reason}`. Every pair gets a real link probe, because two bind mounts of one filesystem share a device ID yet refuse links across them. `result` is `yes`, `no`, `unknown` (Bindery could not write a test file in the download folder) or `missing` (the library folder does not exist and was not probed).
* `primaryFix` is the fix of the first failure, or of the first warning when nothing failed.

Bindery only looks at the filesystem (a stat, a directory listing for a letter
case mismatch, a temporary write probe and a hardlink probe) when the remapped
path is at or under `BINDERY_DOWNLOAD_DIR`, `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` or
a library root, both as written and after following symbolic links. A client
that reports any other folder gets `local_path: fail` saying so, and nothing
there is touched. A symbolic link in the path that does not resolve is refused
rather than followed. Each filesystem phase has a 10 second deadline; a folder
on a mount that stops answering comes back `unknown` instead of holding the
request. At most four filesystem phases can be waiting at once across all
requests; past that a check answers `unknown` saying a previous check is
still waiting for the filesystem. SABnzbd is asked for `complete_dir` and the one category
only, Transmission for `download-dir` only, and Deluge for its three download
location keys and the label's options only. NZBGet's `config` call cannot be
filtered on the server, so its reply is decoded keeping only the folder and
category keys. Error text from a client passes through the same secret
redaction as other outbound errors, and the stored API key and password are
removed from every sentence and path in the response. SABnzbd answers
`client_path: unknown` rather than a failure when its key is an NZB key, which
cannot read folder settings.

### Notifications, backups, system

```
GET    /api/v1/notification                       list webhooks (admin)
POST   /api/v1/notification                       create (admin)
GET    /api/v1/notification/{id}                  fetch one (admin)
PUT    /api/v1/notification/{id}                  update (admin)
DELETE /api/v1/notification/{id}                  remove (admin)
POST   /api/v1/notification/{id}/test             fire a test event (admin)

POST   /api/v1/backup                             snapshot the SQLite database (admin, optional {"label": "..."})
GET    /api/v1/backup                             list stored backups (admin)
DELETE /api/v1/backup/{filename}                  delete one backup (admin)
POST   /api/v1/backup/{filename}/restore          stage a backup for the next restart (admin, X-Confirm-Restore: true)
GET    /api/v1/system/status                      version, commit, build date, newest published release, image cache size, Hardcover feature state
POST   /api/v1/library/scan                       start a library scan in the background (202)
GET    /api/v1/library/scan/status                summary of the last library scan, paths included (admin)
GET    /api/v1/library/unmatched                  books the scan could not match, one row per book (admin)
GET    /api/v1/library/unmatched/summary          pending, ignored and adopted counts plus scan status (admin)
POST   /api/v1/library/unmatched/{id}/adopt       register the row's files in place against a book (admin)
POST   /api/v1/library/unmatched/{id}/undo        reverse an adoption exactly (admin)
POST   /api/v1/library/unmatched/{id}/ignore      set a pending row aside (admin)
POST   /api/v1/library/unmatched/{id}/unignore    return an ignored row to pending (admin)
POST   /api/v1/library/unmatched/ignore           ignore pending rows by {"ids":[..]} or {"authorFolder":"..."} (admin)
GET    /api/v1/system/logs                        app log lines (admin)
GET    /api/v1/system/logs/export                 the same rows as a downloadable file (admin)
GET    /api/v1/system/loglevel                    current log level (admin)
PUT    /api/v1/system/loglevel                    runtime log-level switch, debug/info/warn/error (admin)
GET    /api/v1/images?url=<encoded>               proxied + cached cover image (30-day TTL)
```

`GET /api/v1/library/scan/status` returns the stored summary of the most recent
scan: the counts, the library roots it walked and the path of every unmatched
file. That is server filesystem layout, so the route is admin only in the same
way as `/system/storage`, and a non admin gets `403` (#2361). A `404` means no
scan has run yet. The unmatched files themselves moved to
`/library/unmatched`: the summary now carries `unmatched_units`,
`ignored_units` and `units_truncated`, and `unmatched_files` is always an
empty list, kept for one release so an older cached web bundle still parses.

#### Library adoption

`GET /api/v1/library/unmatched` lists the books a library scan could not
match. A row is a book: an audiobook folder, a disc set, or same named ebook
files are one row. Query parameters, all optional:

| Parameter | Values |
|---|---|
| `state` | `pending` (default), `ignored`, `adopted` |
| `reason` | `author_not_in_library`, `no_candidate_books`, `no_title_match`, `no_title_parsed` |
| `authorFolder` | first folder under the library root |
| `format` | `ebook`, `audiobook` |
| `search` | words matched against title, author and path |
| `sort`, `dir` | `score` (default), `title`, `folder`, `files`, `size`, `seen`; `asc` or `desc` |
| `limit`, `offset` | page, at most 250 rows |
| `facets` | `1` adds grouped counts by reason, format and top author folders |

The response is `{items, total, facets?, summary, scan}`. Each item carries
`id`, `kind`, `format`, `fileCount`, `sizeBytes`, `relPath`, `rootPath`,
`authorFolder`, `parsedTitle`, `parsedAuthor`, `reason`, up to three
`candidates` (`{book, score}`, a title similarity from 0 to 1), `state`, the
adopted `book` if any, `bookCreated`, `authorCreated` and the first 20
`members` file names. No provider is called to build it.

`POST /api/v1/library/unmatched/{id}/adopt` takes either `{"bookId": 12}` for
a book already in the library or `{"foreignBookId": "...", "foreignAuthorId":
"...", "authorName": "..."}` for a metadata result. An optional `"format"`
must equal the row's format; audio files cannot be adopted as an ebook. No
field is a path. The files are registered in place (an audiobook folder as its
folder, a disc set disc folder by disc folder), nothing is moved and no search
starts. A book added from metadata is created unmonitored with its media type
set to the files' format. Every side effect is recorded on the row before the
next, so an adoption interrupted by a crash is reversed at the next start.
Answers:

| Status | Meaning |
|---|---|
| `200` | the updated row |
| `400` | neither or both of `bookId` and `foreignBookId`, or a format that is not the files' format |
| `404` | no such row, or the book is gone |
| `409` | the row is not pending (the body names its `state`), or a file already belongs to a book |
| `422` | a file is gone, is not a regular file, or resolves outside the library folders |
| `502`, `503` | the metadata provider failed or the primary provider is down |

`POST .../undo` removes exactly the book file entries the adoption made, each
only while it still belongs to the book it was registered to, and a book or
author it created when nothing else holds them (books by the author count
whether excluded or not) and the book has not been used since (monitored,
edited, linked to a series, searched or downloaded). A kept book is reported in
the response's `message`. The row returns to pending only once every step
succeeded; a `503` means a step failed (a busy database, say), the adoption
is still on record and is completed automatically within a few minutes, and
a `409` means another request took the row before the undo finished. An adopt
that fails and cannot reverse what it did also answers `503` and is cleaned up
the same way. Adopt, undo, ignore and unignore each claim the row with a compare and
swap, so a double click or two admins acting at once get one `200` and one
`409`.

#### Webhook payload

Every event POSTs a JSON body with a consistent shape so relays render it
without a custom template:

| Field | Meaning |
|-------|---------|
| `eventType` | `grabbed` \| `bookImported` \| `upgrade` \| `downloadFailed` \| `health` \| `bookAnnounced` \| `requestCreated` \| `test` — present on **every** event |
| `title` | what happened, e.g. `Release Grabbed`, `Book Imported`, `Download Failed` |
| `message` | the subject, e.g. `The Way of Kings · Brandon Sanderson` |
| `body` | alias of `message` (Apprise requires a `body` field) |
| `item` | the raw release/book name (the title before it was moved into `message`) |
| `format` | `ebook` \| `audiobook` on `bookImported` and `upgrade`. **Omitted for Apprise targets only** (a URL with a `/notify` path segment) — Apprise reserves `format` for the body markup and rejects anything but `text`/`html`/`markdown` with HTTP 400. Every other consumer still receives it |
| `mediaFormat` | the same value as `format`, always present. Use this one if your relay is Apprise, or if you want a key that is never stripped |
| event extras | `author`, `size`, `path`, `status`, `clientId` when relevant |
| `requestCreated` extras | `kind` (`book` \| `author`), `username`, `mediaType`, `requestId`. `title`, `author` and `username` have control and invisible characters removed, are length capped, and have `@`, `<`, `>`, `[` and `]` replaced with fullwidth lookalikes, and the `://` of any URL broken the same way, so a title or username cannot mention a channel, use a Slack escape such as `<!channel>`, form a markdown link or autolink a URL. The same request from the same person notifies at most once an hour, and one requester at most 10 times an hour. The event is off on every webhook until its **Request** toggle is turned on |

Which events a webhook receives is set per notification by `onGrab`,
`onImport`, `onUpgrade`, `onFailure`, `onHealth`, `onBookAnnounced` and
`onRequestCreated`. The last two default to `false`, and migrations 087 and
089 set them to `false` on every notification that existed before them, so an
upgrade sends nothing new until an admin turns them on.

**`bookAnnounced`** is sent once per author run when a refresh (manual, bulk,
Refresh all, relink) or scheduled discovery adds books to an author that had
at least one book row, excluded ones included, before the run started. The
first population of a new author, the refill of an author whose books were all
deleted, and the single book add never send it. Two syncs of the same author
that overlap run their writes one after the other, so a work is announced
once.

| Field | Meaning |
|-------|---------|
| `title` | `New Book Found` or `New Books Found` |
| `message` | `Author: Title one, Title two and N more` |
| `author`, `authorId` | the author the books were added to |
| `count` | how many books the run added |
| `books` | up to 10 entries of `{id, title, foreignId, monitored, releaseDate}`; `releaseDate` is `YYYY-MM-DD` and omitted when unknown; `monitored` says whether the book will be searched for |
| `more` | how many added books are not listed |

Titles, the author name and foreign ids come from the metadata provider, which
for OpenLibrary is publicly editable. They are stripped of control and
bidirectional override characters, collapsed to one line, and capped (200
characters for titles and names, 100 for ids) before they are sent.

**ntfy:** set the notification's **topic** field and point the URL at the ntfy
server root (e.g. `https://ntfy.sh`). Bindery then POSTs the JSON body with a
`topic` field to the root, which ntfy renders natively. Without a topic it POSTs
to the URL as-is, so a topic URL would show the raw JSON — use the topic field
or ntfy message-templating headers (`X-Title`, `X-Message`) instead.

### Settings

```
GET    /api/v1/setting                            list stored settings
GET    /api/v1/setting/{key}                      read one stored setting
PUT    /api/v1/setting/{key}                      write one setting (admin), body {"value": "..."}
DELETE /api/v1/setting/{key}                      delete one setting (admin)
GET    /api/v1/settings/descriptors               describe every key Bindery knows (admin)
```

Secrets (`*.api_key`, `*.api_token`, `auth.*`, and the rest of
`isSecretSetting`) never appear in a list or a read, and settings whose value is
a server filesystem path are returned to admins only.

**`PUT` refuses a key Bindery does not know**, with `400` and the key named.
Before this, an unrecognised key was stored and then read by nothing, so a typo
such as `serch.interval` saved, reported success and silently did nothing
forever. Reads and deletes are unchanged, so a row written by a different build
is still listed and can still be removed.

`GET /api/v1/settings/descriptors` is the list of keys that will be accepted. It
carries no stored values, only the shape of each key:

| Field | Meaning |
|-------|---------|
| `key` | the settings key |
| `type` | `string` \| `bool` \| `int` \| `duration` \| `enum` |
| `default` | what Bindery behaves as if the key held when it is unset |
| `values` | accepted values, for `enum` keys |
| `min`, `max` | bounds, for `int` and `duration` keys |
| `description` | one line explaining what the key does |
| `restartRequired` | `true` when the key is read once at startup, so a change is stored now and takes effect at the next restart |
| `state` | `active` (something reads it), `internal` (Bindery writes it as its own bookkeeping, not a knob), `inert` (accepted and stored and read by nothing) |
| `secret` | the value is never returned, not even to an admin |
| `adminOnly` | only an admin may read the value |
| `writable` | `PUT /api/v1/setting/{key}` accepts this key |

A key marked `inert` is kept so existing rows and existing clients keep working.
Do not offer it as a control: nothing will happen.

`authors.discovery.interval` is `off` or a duration from `24h` to `720h`
(default `off`). It sets how often each monitored author is checked for new
books by the scheduled discovery job. Unset means `off` as well, so the job
runs only once a duration is stored, and the value is read on every hourly
tick, so `restartRequired` is `false`.

### Auth and users (admin)

```
GET    /api/v1/auth/status                        public — am I logged in?
GET    /api/v1/auth/csrf                          fetch a CSRF token for browser flows
POST   /api/v1/auth/login                         username + password
POST   /api/v1/auth/logout
POST   /api/v1/auth/setup                         first-run admin creation (one-shot)
PUT    /api/v1/auth/mode                          switch enabled/local-only/disabled/proxy (admin)
POST   /api/v1/auth/password                      change own password
POST   /api/v1/auth/apikey/regenerate             rotate the instance API key (admin)

GET    /api/v1/auth/oidc/providers                list configured providers
PUT    /api/v1/auth/oidc/providers                update providers (admin)
GET    /api/v1/auth/oidc/{provider}/login         start an OIDC login
GET    /api/v1/auth/oidc/{provider}/callback      OIDC redirect target

GET    /api/v1/auth/users                         list users (admin)
POST   /api/v1/auth/users                         create (admin)
DELETE /api/v1/auth/users/{id}                    delete (admin)
PUT    /api/v1/auth/users/{id}/role               change role (admin), body {"role": "admin"|"user"|"requester"}
PUT    /api/v1/auth/users/{id}/reset-password     reset (admin)
```

### Requests

The requester role's API, and the admin queue that decides requests. See
[Requester](multi-user.md#requester) for what the role can and cannot do.

```
POST   /api/v1/requests                           ask for a book or an author
GET    /api/v1/requests                           the caller's own requests, newest first (limit, offset)
DELETE /api/v1/requests/{id}                      withdraw the caller's own pending request
GET    /api/v1/requests/library                   read only library projection (search, limit, offset)

GET    /api/v1/requests/queue                     every user's requests (admin), status=pending|approved|declined|all
GET    /api/v1/requests/pending-count             {"count": n} for the nav badge (admin)
POST   /api/v1/requests/{id}/approve              add what was asked for, owned by the requester (admin)
POST   /api/v1/requests/{id}/decline              decline, body {"reason": "..."} optional (admin)
```

`POST /api/v1/requests` takes exactly three fields and refuses any other with
`400`, from a body of at most 4 KiB:

```json
{"kind": "book", "foreignId": "OL27482W", "mediaType": "ebook"}
```

`kind` is `book` or `author`, `foreignId` is the provider id a metadata search
returned, and `mediaType` is `ebook`, `audiobook`, `both` or empty. Bindery
looks the id up at the metadata provider and stores the title and author the
provider reports. Answers:

| Status | Meaning |
|--------|---------|
| `201` | the request, as below |
| `409` | already in the library, already requested by this user (pending or declined); the `error` field says which |
| `429` | this user already has `requests.max_pending_per_user` requests waiting (default 25), or a requester made creates or searches faster than the per user allowance (burst of 20, then one every 3 seconds; `Retry-After` says when). The cap is checked in the same statement as the insert, so concurrent creates cannot pass it |
| `502`, `404` | the provider did not answer, or has no such id |

A request as the API returns it:

| Field | Meaning |
|-------|---------|
| `id`, `kind`, `foreignId`, `mediaType` | as requested |
| `title`, `authorName` | from the provider at request time (for an author request, `title` is the author's name) |
| `status` | `pending` \| `approved` \| `declined` |
| `declineReason` | the admin's reason, when declined with one |
| `createdAt`, `decidedAt` | timestamps |
| `fulfilled` | approved and on disk: the book imported, or at least one of the author's books |
| `booksTotal`, `booksImported` | an approved author request's progress |
| `username`, `resultBookId`, `resultAuthorId` | admin queue only |

`GET /api/v1/requests/library` returns `{items, total, limit, offset}` where
each item has only `id`, `title`, `authorName`, `series`, `seriesPosition`,
`coverUrl`, `status` and `formats` (the formats with a file on disk). With
`BINDERY_ENFORCE_TENANCY` on it is scoped like the Books list.

`POST /api/v1/requests/{id}/approve` takes the admin's choices, all optional:
`metadataProfileId`, `qualityProfileId`, `rootFolderId`,
`audiobookRootFolderId`, `monitorMode`, `monitorLatestCount` and
`monitorNewItems` apply to an author request; `mediaType` and `searchOnAdd`
apply to both kinds. The approval is claimed atomically, so of two concurrent
approvals one adds and the other gets `409`, and a running approval renews its
claim so a slow add cannot be taken over. A request whose item reached the
library in the meantime answers `409`, and a failed add leaves the request
pending. Unknown fields are refused with `400`.

A requester may call only `/requests`, `/requests/{id}` (DELETE),
`/requests/library`, the three metadata searches (`/search/author`,
`/search/book`, `/book/lookup`, rate limited per user), `/images` (rate limited per user with a larger allowance), `/health`
and the session routes under `/auth`. Every other route answers `403` for
that role, and `/opds` does too.

### Arr-compatible queue

```
GET    /api/queue                                 Sonarr/Radarr-style queue payload
```

This endpoint sits **outside** `/api/v1/` and matches the queue contract used by Harpoon and similar *arr-aware tools. It returns `totalRecords`, supports pagination and sort, and surfaces per-record `size`, `sizeleft`, `status`, `client`, `remote ID`, and `protocol`. It sits behind the same authentication as `/api/v1`, so an API key, a session cookie or an auth mode that admits the caller all work. It is a `GET`, so no CSRF token is needed.

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

OPDS authenticates via HTTP Basic with a Bindery username and that account's password, or with the API key in an `X-Api-Key` header or an `?apikey=` query parameter. The key is not accepted as the Basic password. KOReader, Moon+ Reader, Aldiko, and other OPDS-capable apps work out of the box.

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
