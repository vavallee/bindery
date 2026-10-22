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

Rules 1 and 2 also require the host name the request was sent to (the `Host` header, plus `X-Forwarded-Host` from a trusted proxy) to be one a site on the internet cannot have: an IP address, `localhost`, a single label name such as `bindery` or `nas`, a name under `.local`, `.lan`, `.home.arpa`, `.internal` or `.localdomain`, the host of `BINDERY_OIDC_REDIRECT_BASE_URL`, or a name listed in `BINDERY_ALLOWED_HOSTS`. Any other name gets `403` with an error naming that variable, and `GET /api/v1/auth/status` reports `authenticated: false` with `hostNotAllowed: true` and the name in `refusedHost`. The OPDS feed applies the same rule to its own mode grants and answers a refused name with its usual `401` Basic challenge. An API key or a session is accepted under any name. This stops a web page from reaching Bindery through the visitor's browser by pointing its own DNS name at Bindery's address (DNS rebinding). See [DEPLOYMENT.md](DEPLOYMENT.md#host-names-in-local-only-and-disabled-mode).

Otherwise the server returns `401`. Browser sessions also need a CSRF double-submit token on mutating requests (anything other than `GET`, `HEAD` and `OPTIONS`); API-key clients are exempt from CSRF.

Non-browser clients (curl, scripts, mobile apps) authenticating via API key do **not** need to send an `X-Requested-With: bindery-ui` header — that header is required only for browser sessions to satisfy the CSRF gate. The auth endpoints listed above (`/auth/login`, `/auth/logout`, `/auth/setup`, `/auth/status`, `/auth/csrf`) are exempt from the `X-Requested-With` check entirely, since there is no session to protect at that stage.

The API key lives in **Settings → General → Security**. Regenerating it invalidates every existing consumer.

The Calibre bridge routes under `/bridge/v1` are outside all of this: they take only the Calibre plugin key as a Bearer token, in every auth mode. See [Calibre bridge (pull)](#calibre-bridge-pull).

## Error format

Every error from `/api` and `/api/v1` is JSON with an `error` string:

```json
{"error": "indexer not found"}
```

The text is written for people to read and is safe to show. Messages that come from Bindery itself (validation, not found) are specific. Anything that came from the database, the filesystem or another service is replaced with a fixed message and logged on the server, so an error never carries a connection string, a path or an upstream API key.

| Status | Meaning |
|---|---|
| 400 | The request is invalid: a malformed body is `invalid request body`, other validation failures name the field |
| 401 | Not authenticated |
| 403 | Authenticated but not allowed, or a browser request missing the CSRF header |
| 404 | No such route (`{"error": "not found"}`), or no such row. A row you may not see is also 404, never 403 |
| 405 | The route exists but not for that method (`{"error": "method not allowed"}`). The `Allow` header lists the methods that do work |
| 409 | The request conflicts with current state |
| 413 | The request body is larger than the limit, `{"error": "request body too large"}`. JSON bodies are limited to 1 MiB; uploads have their own, larger limits. This used to be a 400. Endpoints are being moved over one group at a time: a few older ones, such as creating an author, still answer `400 invalid request body` for an oversize body |
| 429 | Too many attempts |
| 500 | A server fault: `{"error": "internal server error"}`. A store error is never reported as a 404. When the fault was an unexpected crash the body also carries `request_id`, which matches the `request_id` on the log entry in Settings, Logs |
| 502 | Another service failed: a metadata provider, an indexer, a download client or Prowlarr. The message is generic, except on the connection test endpoints, which relay the first line of what the other service said with credentials redacted, at most 200 characters |

Unknown paths under `/api` and `/api/v1` answer the JSON 404 above, and a wrong method answers the JSON 405. They used to be plain text and an empty body. An unauthenticated request to an unknown path still gets `401`, not `404`.

A few endpoints are tests and report a failed check inside a `200`, because the request itself worked:

| Endpoint | Failure looks like |
|---|---|
| `POST /api/v1/indexer/{id}/test` and `POST /api/v1/indexer/test` | `{"ok": false, "error": "..."}` |
| `POST /api/v1/prowlarr/{id}/test` | `{"ok": false, "error": "..."}`. `ok` is a JSON boolean; it was the string `"true"` or `"false"` before |
| `POST /api/v1/auth/oidc/test-discovery` | `{"ok": false, "error": "..."}` |

The download client test endpoints are not in this list: they answer `502` with the error when the client cannot be reached.

Outside these trees the format differs on purpose. The Calibre bridge adds a machine readable `code` (see [Calibre bridge (pull)](#calibre-bridge-pull)). OPDS feeds and file downloads answer in the protocol they serve, and the cover image proxy errors are plain text.

## Endpoint catalogue (selection)

### Authors

```
GET    /api/v1/author                             list authors (paginated, filterable)
POST   /api/v1/author                             add an author (triggers async book fetch)
POST   /api/v1/author/book                        add one book by foreign id (creates its author if needed)
POST   /api/v1/author/bulk                        bulk add/update
GET    /api/v1/author/{id}                        author detail
PUT    /api/v1/author/{id}                        update monitored / metadata profile, or the bio: `description` set by hand is kept as the user's own and no refresh replaces it; an empty string clears it for the provider to fill again (#3287). 409 when the author changed since it was read, such as a scheduled refresh in between: reload and save again
DELETE /api/v1/author/{id}                        remove (with optional file delete)
POST   /api/v1/author/{id}/refresh                re-pull profile and works, skipping the metadata cache; 409 while a sync for that author is running
POST   /api/v1/author/{id}/photo                  set the photo from the request body, as `POST /book/{id}/cover` does for a cover (raw JPEG, PNG, WebP or GIF, max 10 MB; 413 over the limit, 415 for anything else); stored locally and kept as the user's own, so no refresh or enricher replaces it. 409 when the author changed since it was read. Answers the author (#3287)
DELETE /api/v1/author/{id}/photo                  remove a photo set with the POST above, then refresh the author's profile as its Refresh button does, so the provider's or an enricher's photo shows at once. 409 for a photo the user didn't upload, or when the author changed since it was read. The stored file stays, since the cover store is shared with book covers. Answers the author (#3287)
GET    /api/v1/author/{id}/catalogue-reconciliation
                                                    preview stale metadata-only Wanted rows
POST   /api/v1/author/{id}/catalogue-reconciliation
                                                     recheck and remove selected preview rows
 GET    /api/v1/author/{id}/duplicate-candidates
                                                     read-only groups of titles that look like the same book (#1970)
 GET    /api/v1/author/{id}/relink-upstream/candidates
                                                    search metadata candidates for manual relink
POST   /api/v1/author/{id}/relink-upstream        re-bind to a different foreign ID
GET    /api/v1/author/{id}/aliases                list merged-in alias rows
POST   /api/v1/author/{id}/merge                  merge another author into this one
```

Each author in `GET /api/v1/author` carries `statistics.bookCount` (the author's
books, scoped to the caller like `GET /book`) and `statistics.availableBookCount`
(#3092), the subset in the library by the same rule as `GET /book?status=imported`:
a book with at least one tracked file, including a `wanted` book that already holds
one of its formats. `wantedBookCount` is not filled on the list.

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

The same 409 and body come back when the foreign id is new but the work is
already there under another id and the same author, for example adding
"Ready Player One" when "Ready Player One (Movie Tie-In)" is in the library
(#3466). `existingBookId` is the row the request matched, which keeps its own
foreign id.

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
instead of starting a second sync (#2601). It is also set from the moment
`POST /api/v1/author` answers, for the catalogue sync the add starts, so a page
opened straight after the add sees it (#3058).

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
and returns `candidates`, `indeterminateRows`, a reason-count summary,
protection counts, and `providerComplete`. Each `indeterminateRows` entry has a
`bookId`, `title`, `metadataProvider`, and display-only `reason`. These rows are
informational: they are kept because evidence is incomplete and are never
deletion candidates. Stable indeterminate reasons are `language_unknown`,
`language_evidence_lookup_failed`, `edition_evidence_unavailable`,
`partial_catalogue`, and `unmatched_cross_provider`. A partial provider result
never treats absence as a reason
to remove a row. A complete result can make an absent same-provider row a
candidate, but an unmatched row from another provider is kept as indeterminate:
provider migration alone is not deletion evidence. Explicit profile rejections
still apply when the row can be correlated across providers. The POST route
accepts the IDs from the preview:

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

Any other provider failure on a relink, a relink candidate search, a book
rebind, an audiobook enrich or an add book answers **502** with the fixed
message `metadata provider unavailable`, except while the provider is paused
(a daily quota or a rejected token), which says so and when it ends. The
provider's own error text embeds
its request URL (and so an API key and the resolver address), so it goes to the
Bindery log and not to the client.

`GET /api/v1/author/{id}/duplicate-candidates` reports groups of the author's
books whose titles look like the same book, for human review (#1970). It is
read-only: it never writes, and the review UI's only action is the existing
`PUT /book/{id}/exclude`. Titles are compared with an aggressive fold (case,
punctuation, and diacritics ignored; `&` expanded to "and"), and a pair joins a
group when any of these match: `alnum-equal` (identical after the fold),
`article-strip` (identical after dropping a leading article),
`edition-suffix` (identical after dropping a trailing edition marker), or
`substring` (one title is the whole main title or subtitle of the other, split
at a colon, bracket, or spaced dash, with length guards; "Foundation" does not
match "Foundation and Empire"). `substring` is additionally suppressed when
both books are known, different positions in the same series, so "Mistborn"
at position 1 does not match "Mistborn: The Well of Ascension" at position 2.
Groups are linked transitively, so A≈B and B≈C lands in one group.
The response is:

```json
{
  "authorId": 42,
  "count": 1,
  "groups": [
    {
      "key": "themartian",
      "rules": ["article-strip", "substring"],
      "books": [
        { "id": 101, "title": "The Martian", "excluded": false, "rules": ["article-strip"] },
        { "id": 102, "title": "Martian", "excluded": true, "rules": ["article-strip"] }
      ]
    }
  ]
}
```

Each `books` entry is the full book object plus a per-book `rules` list; the
group's `rules` is the union across its members. Groups whose members are all
excluded are omitted, and `count` is the number of groups returned.

Each group also carries review evidence (#2999), built only from data Bindery
already stores; no provider is called:

| Field | Where | Meaning |
|-------|-------|---------|
| `authorId`, `authorName` | group | the author the group belongs to |
| `hasFiles` | book | the row has at least one file in the library |
| `evidence.files` | book | `{kind, format}` per file: `kind` is `ebook` or `audiobook`, `format` the extension (`epub`, `m4b`), empty for an audiobook folder |
| `evidence.isbns`, `evidence.isbnCount` | book | edition ISBNs as ISBN-13 where the check digit allows, at most five shown; `isbnCount` is the full number compared |
| `evidence.asins` | book | the book's ASIN plus its editions' ASINs |
| `evidence.series` | book | `{seriesId, title, position}` per series membership |
| `evidence.year` | book | release year, omitted when unknown |
| `signals` | group | agreements and conflicts between the non-excluded rows: `{kind, conflict, bookIds, values}` |
| `conflict` | group | true when any signal is a conflict |
| `keeperId` | group | the one non-excluded row with files; omitted when no row or several rows have files |
| `suggestedExcludeIds` | group | the non-excluded rows without files, offered as one confirmed exclusion; empty unless there is a `keeperId`, no conflict, and positive evidence tying every empty row to the keeper (a shared ISBN or ASIN, the same series position, or titles that match by a rule stronger than `substring`); never contains a row with files |
| `suggestionWithheld` | group | why there is no suggestion: `no-files`, `several-with-files`, `conflict` or `no-evidence`; omitted when there is one |

Signal kinds are `shared-isbn`, `shared-asin` and `same-series-position`
(agreements: evidence the rows are one book) and `series-position-conflict`,
`year-conflict` (release years more than one year apart) and
`language-conflict` (languages that differ after normalising codes, so `en`,
`eng` and `English` agree; `und`, `mul`, `mis` and `zxx` count as unknown).
Series positions compare numerically, so `1` and `1.0` are one position.
Nothing is acted on automatically: the review UI excludes rows only through
`PUT /book/{id}/exclude` or `POST /book/bulk` with `"action": "exclude"`, after
a person confirms. For the empty rows it sends `"expectNoFiles": true`, which
makes the bulk exclude skip any book that has a file by the time the request
runs, reporting it with `"code": "has_files"` instead of excluding it.

`GET /api/v1/library/duplicate-candidates?limit=25&offset=0` returns the same
groups for every author in one paginated list (#2999), ordered by author name
then group key:

```json
{ "groups": [ ... ], "total": 124, "count": 25, "limit": 25, "offset": 0 }
```

Detection is the per-author scan run once per author, so a title is only ever
compared with titles by the same author, and the groups match what each
author's own window reports. `total` counts groups across all pages; `limit`
defaults to 25 and is capped at 100. The page's books are full book objects
with `description` left empty. It is not admin only, matching the per-author
route; with `BINDERY_ENFORCE_TENANCY` on, a non-admin sees groups for the
authors they own and unowned authors only, the same authors the per-author
route would open for them. The scan reads one thin row per book and loads full
rows and evidence only for the page it returns, in batched queries. The sorted
group list is cached per owner scope under a fingerprint of the books, series
links and authors tables, for at most two minutes, so turning pages does not
rescan; any exclusion, import, new book or series change produces a new
fingerprint and the next request rescans.

### Books

```
GET    /api/v1/book?status=wanted                 filter by status (wanted, imported, skipped); `imported` is the In library view and also lists a `wanted` book that already has at least one tracked file (of that format when `mediaType` is set), so a partly owned dual format book appears under both
POST   /api/v1/book/bulk                          bulk monitor / status flip / exclude / unexclude (`"expectNoFiles": true` skips books that have files on `exclude`)
GET    /api/v1/book/{id}                          book detail (with editions, history, formats; `importInFlight: true` while a download for it is still on its way into the library)
PUT    /api/v1/book/{id}                          update monitor / status / metadata
DELETE /api/v1/book/{id}                          remove from library
DELETE /api/v1/book/{id}/file                     delete imported file(s) on disk (`?format=ebook|audiobook` scopes to one format; `?path=…` deregisters one tracked path WITHOUT deleting anything on disk)
PUT    /api/v1/book/{id}/exclude                  exclude from future searches
GET    /api/v1/book/{id}/series-exclusions        the series the book was taken out of: [{seriesForeignId, seriesId, seriesTitle, position}]; seriesId is 0 when nothing resolves the id any more
POST   /api/v1/book/{id}/rebind                   re-link to a different metadata record
POST   /api/v1/book/{id}/enrich-audiobook         pull narrator/duration/cover from Audnex
POST   /api/v1/book/{id}/cover                    set the cover from the request body (raw JPEG, PNG, WebP or GIF, max 10 MB); answers 413 over the limit and 415 for anything that is not one of those four; stored locally, replaces the current cover, and a refresh does not replace it. Answers the full book, as `GET /book/{id}` does
POST   /api/v1/book/{id}/search                   manual indexer search
GET    /api/v1/book/{id}/file                     download the imported file (auth required; `?path=…` serves one specific tracked file, for a book holding several of a format; `?format=ebook|audiobook` picks the format on dual-format books; `?path=` wins when both are sent)
GET    /api/v1/book/{id}/calibre                  where the book stands in the Calibre delivery queue (see Calibre below)
```

`POST /book/{id}/search` answers
`{"results": [...], "filtered": [...], "filteredTotal": N, "debug": {...}}`
(#2334).

* `results` are the releases that passed the filters. Each carries `approved`
  and, when it is not approved, a `rejection` reason from the blocklist,
  already imported and allowed formats checks. Every one of them can still be
  grabbed.
* `filtered` are the releases the title and author relevance filter and the
  language filter removed. Each has the same fields as a `results` item, with
  `approved: false` and `rejection` holding the filter's reason. They are not
  run through the blocklist, already imported or allowed formats checks. They
  grab like any other release: post the `guid`, `title`, `nzbUrl`, `indexerId`
  and `bookId` to `POST /queue/grab`, and the server uses the download URL it
  recorded for that `guid`, for admin sessions and user accounts alike. Releases
  that are not books (video, a movie or console category, a raw usenet posting
  or a chapter fragment) are never in `filtered`; they show in
  `debug.filters` only.
* `filteredTotal` is how many the filters removed. `filtered` holds at most 100
  of them, the likeliest matches first (for a dual-format book, alternating between the two formats), so `filteredTotal` can be larger than
  `filtered.length`.

Clients written before this ignore the two new keys and see no change. Automatic
searches never see a filtered release. The free text search
(`GET /indexer/search`) does not return `filtered`.

Each book carries `averageRating` (community rating, 0 when the provider has none)
and `ratingsCount` (#3124). `GET /book?sort=` also accepts `rating-desc` and
`rating-asc`; unrated books sort last in both directions, and `rating-desc` breaks
ties by `ratingsCount`. OpenLibrary results of `GET /search/book` carry the
same two fields.

### Series

```
GET    /api/v1/series                             list series with their linked books
GET    /api/v1/series/{id}                        one series
POST   /api/v1/series                             create a series (admin): {"title":"Saga"} makes a plain one; add a "hardcover" object (the fields of a `GET /series/hardcover/search` result: foreignId, providerId, slug, title, authorName, bookCount) to create it already linked, with title then optional (it defaults to the Hardcover title). Answers 201 with the series and its hardcoverLink; 409 {error, seriesId} when that Hardcover series is already in the library (nothing is created, and no Hardcover call is made); 400 for a foreignId that is not `hc-series:<digits>` or for oversized fields; 404 when enhanced Hardcover is off or Hardcover has no such series; 502 when Hardcover cannot be reached (nothing is created). No books are added
POST   /api/v1/series/{id}/fill                   add the series' missing books as wanted (admin); answers {queued, skippedByProfile, skippedSplitParts}
POST   /api/v1/series/{id}/split-parts/unmonitor  unmonitor the split edition parts of books already in the series, listed as splitEditionPartBookIds on the series (admin); answers {unmonitored}
PATCH  /api/v1/series/{id}                        monitor / unmonitor (admin)
POST   /api/v1/series/{id}/merge                  merge other series into this one: {"sourceIds":[..],"title":"optional rename","dryRun":true} previews (admin)
POST   /api/v1/series/{id}/books                  add a book or update its position: {"bookId":1,"positionInSeries":"2","primarySeries":true}; positionInSeries is optional (admin)
DELETE /api/v1/series/{id}/books/{bookId}         take a book out of the series, and keep it out on refresh (admin)
POST   /api/v1/abs/import                         start an Audiobookshelf import (admin): {"dryRun":true,"libraryIds":[..],"full":true}, all optional. libraryIds must be a subset of the configured libraries (400 on an unknown id), full re-runs the Hardcover series match for every item
```

Taking a book out of a series is remembered (#2554): a refresh that still
reports the book in that series, under its own provider id or one merged into
it, leaves it out. Adding the book back with `POST /series/{id}/books` forgets
that, and so does an admin unlocking all of the book's fields (`PUT /book/{id}`
with `"lockedFields": []`; another user's unlock leaves the book's series alone). Adding a book as primary makes it the book's only
primary series. `GET /book/{id}/series-exclusions` lists what was taken out.

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
GET    /api/v1/wanted/missing                     list wanted-but-missing books. A bare array, every one of them, unless `limit` is passed:
                                                  `?limit=50&offset=0&search=&includeExcluded=` answers {"items":[..],"total":N,"limit":50,"offset":0}.
                                                  `total` counts every wanted book you can see that matches `search` and `includeExcluded`,
                                                  whatever the page. `limit` is capped at 500; `search` and `offset` apply only with `limit`
POST   /api/v1/wanted/bulk                        bulk operations on wanted
```

Some bulk actions start an automatic search (search and grab) for the books
they touch, and the response is written before any indexer is asked: the
searches run on a background pool afterwards, so `"ok": true` means the action
was accepted, not that a search finished (#2154). These are the actions that
can search, and when:

* `POST /book/bulk` with `"action": "search"`: each book.
* `POST /book/bulk` with `"action": "monitor"`: a book that was wanted and
  becomes monitored, the same immediate search a single book monitor fires.
* `POST /wanted/bulk` with `"action": "search"`: each book.
* `POST /author/bulk` with `"action": "search"`: every monitored wanted book of
  the author.

For those actions an `ok` entry carries one of two extra fields:

* `"queued": true`: at least one search for this id is on its way. It is set
  only when that search will actually run: a searcher is configured,
  automatic grabbing is on, and the book still needs a format.
* `"searchSkipped": "<reason>"`: the action succeeded but no search was
  queued. The reasons are `no_format_needed` (every format the book wants is
  already on disk), `nothing_wanted` (an author with no monitored wanted book
  still needing a format), `auto_grab_disabled` (a monitor while automatic
  grabbing is off) and `no_searcher`.

A `"action": "search"` while automatic grabbing is off is refused instead,
with `"ok": false` and `"code": "auto_grab_disabled"` (#2669). Each search
that runs leaves a `book search finished` line in the log with its outcome,
and shows in `GET /search/last-debug`.

Metadata results say when they are already in the caller's library (#1227).
Each `/search/book` and `/book/lookup` result carries `libraryBookId` when its
`foreignBookId` matches a book the requesting user owns, and each
`/search/author` result carries `libraryAuthorId` when its `foreignAuthorId`
matches a library author visible to the user, by primary id or by an alternate
identifier. Both fields hold the library row id and are omitted otherwise, so a
client can offer "open" for those rows and "add" for the rest. The lookup is
owner scoped: another user's copy does not stamp. If the library lookup itself
fails the search still succeeds, unstamped.

An author in `/search/author` carries the provider's work count as
`statistics.bookCount` when one is known (#3126). Hardcover supplies it as well as
OpenLibrary and Nasjonalbiblioteket, and when several providers return the same
author the surviving record keeps the largest count reported, so the count is not
lost when a provider that reports none wins the merge.

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
GET    /api/v1/search/last-debug                  newest search audit trail you can see (debugging; see below)

GET    /api/v1/prowlarr                           list registered Prowlarr servers (admin)
GET    /api/v1/prowlarr/{id}                      fetch one (admin)
POST   /api/v1/prowlarr                           add a Prowlarr server (admin)
PUT    /api/v1/prowlarr/{id}                      update (admin)
DELETE /api/v1/prowlarr/{id}                      remove (admin)
POST   /api/v1/prowlarr/{id}/test                 probe connectivity (admin); always 200 with a boolean "ok"
                                                  (and "version" when true). When false, "error" is the first
                                                  line of what Prowlarr said, redacted, at most 200 characters
POST   /api/v1/prowlarr/{id}/sync                 import indexers from Prowlarr (admin); 502 with the same
                                                  shortened "error" when Prowlarr fails

GET    /api/v1/rootfolder                         list library roots
POST   /api/v1/rootfolder                         add a new root (admin); 400 says whether the path is
                                                  missing, unreadable or under a file
DELETE /api/v1/rootfolder/{id}                    remove (admin)
```

`GET /search/last-debug` returns the newest search audit trail the caller may
see: their own latest search from a book page's Search button, or the latest
automatic search (scheduled sweep, bulk search, series fill and so on) of a
book they can see, whichever ran last. A request made with the API key sees
every user's interactive searches too, so a script can read the search a user
just ran in the browser. A signed in user never sees another user's
interactive search. The payload says which search it is (#2154):

* `origin`: `interactive` for the Search button, otherwise what started the
  automatic search: `scheduled`, `bulk`, `series-fill`, `author`, `book`,
  `add`, `recommendation`, `list-sync`, `requeue` or `unknown`.
* `bookId`: the book searched for.
* `userId`: the signed in user who ran an interactive search; absent for an
  automatic one.
* `outcome`: an automatic search's result, in the same words as the
  `book search finished` log line (`grabbed`, `no results`,
  `nothing approved` and so on).

Check `bookId` and `startedAt` before reading the rest: an automatic search
that ran after yours will replace it as the newest. 404 means nothing has run
since startup.

#### Quality profiles

```
GET    /api/v1/qualityprofile                     list quality profiles
GET    /api/v1/qualityprofile/{id}                read one
POST   /api/v1/qualityprofile                     create (admin)
PUT    /api/v1/qualityprofile/{id}                update (admin)
DELETE /api/v1/qualityprofile/{id}                remove (admin); 409 while an author uses it
```

A profile is `{id, name, items, cutoff, upgradeAllowed}`. `items` is an array
of `{quality, allowed}` in preference order, best first (#2733). The media
type of each entry is derived from its token on the server (`epub`, `azw3`,
`pdf` and the other ebook containers on one side; `m4b`, `mp3`, `flac`, `m4a`,
`ogg` on the other), so one array holds an ebook list and an audiobook list
and only the relative order inside each kind matters. The web editor writes
the ebook entries first and the audiobook entries after them; any interleaving
is accepted and stored as sent, and a read returns the order that was written.
An unticked entry (`allowed: false`) never makes a release eligible and never
counts in the ranking. A release is judged and ranked only on the formats of
the media type being searched, so an audiobook carrying a PDF booklet is
judged on its audio token for an audiobook search and on its pdf token for an
ebook one. A media type with no entries at all is not constrained: any format
of that kind is accepted and ranked by the built in order, which
`docs/User-Guide-Wiki.md` names. A token Bindery
does not recognise is stored and ignored. Duplicates, an empty `items` and a
list with nothing allowed are refused with `400`. `cutoff` and
`upgradeAllowed` are accepted and stored but read by nothing.

#### Per-indexer daily query cap

`dailyQueryLimit` on an indexer caps how many requests Bindery will send it in a
rolling 24 hours (#2312). Omitted, `null` and `0` all mean no cap. A negative
value is rejected with 400. `GET /indexer` and `GET /indexer/{id}` also return
`dailyQueriesUsed` on capped indexers, which is a display figure summed from the
stored hourly buckets and lags the live tally by up to one flush interval.

#### Per-indexer download client pin

`downloadClientId` on an indexer pins every grab from it to one download client
(#3080). `null` and omitted on create mean no pin, which keeps the usual routing.
Omitted on `PUT` keeps the stored pin, and `null` clears it. The client must exist
and its protocol must match the indexer's (`torznab` is torrent, `newznab` is
usenet), otherwise `POST` and `PUT` answer `400` with the reason. A client that is
disabled is refused for a new or changed pin, but an unrelated edit of an
indexer already pinned to a client that was disabled later still saves.

A pinned grab ignores the client's priority and media type routing. If the pinned
client is disabled, missing or of the wrong protocol, the grab fails and no other
client is tried: `POST /queue/grab`, `POST /queue/{id}/retry` and
`POST /pending/{id}/grab` answer `400` with `code: "indexer_client_unavailable"`.
The message names the indexer, and the client only for an admin caller. The bulk
retry returns the same text per item in its results map instead of a status. A
client that is enabled but does not answer is the usual `502`, with the message
extended to say the indexer is pinned and no other client was tried. The field is
only returned by the admin only indexer endpoints.


Which indexer a grab belongs to is decided once, from the `indexerId` you send
and the release URL's host, before the URL is signed. Signing never overrides
the decision. The rules, in order:

1. An `indexerId` that exists and is pinned always wins. Its pin applies,
   whatever the URL's host is and whoever could sign the URL.
2. An `indexerId` that exists and is not pinned is taken at face value when its
   host matches the release URL's host, even if a pinned indexer shares that
   host. When its host does not match the URL's host and some indexer that does
   match is pinned, the grab is refused. Otherwise it stands.
3. An `indexerId` that matches no indexer (or `null`, as on a retry of a row whose
   indexer was deleted) is treated as no id at all.
4. With no usable id, the indexers whose host matches the URL decide. None: the
   grab is routed as if unpinned. Exactly one: that is the release's indexer, and
   its pin, if any, applies (this finds a recreated indexer, and keyless
   indexers). Several: if any is pinned the grab is refused, because it cannot be
   told which one it came from; if none is pinned nothing changes.

A refusal is the `400` with `code: "indexer_client_unavailable"` and nothing is
sent. The message says the release could not be tied to one indexer (or that the
named indexer does not match the release's host), and lists the pinned indexers
for an admin only. The row's `indexerId` is the decided indexer. The retry
and the pending grab go through the same decision.

The host comparison ignores case, one trailing dot and the port, in any
combination: `prowlarr` and `prowlarr:9696` are the same host. Ignoring a
differing port is deliberate, because a spurious refusal is safer than letting a
private release reach the public client. These do not match, so an id-less grab
through them is routed as if unpinned unless you send `indexerId`: aliases of one
machine (`localhost`, `127.0.0.1` and a DNS name), `www.` and other subdomains,
scheme-less indexer URLs, and punycode against Unicode spellings. On a shared
Prowlarr or Jackett host, one pinned indexer makes id-less API grabs fail for
every other indexer on that host; the web app and the scheduler always send the
indexer and are unaffected.

On save, a client with the wrong protocol is refused only when the request sets
the pin or changes the indexer `type`. Enabling, disabling or reprioritising an
indexer whose pinned client was changed to the other protocol later still saves;
its grabs fail at grab time.

`DELETE /downloadclient/{id}` answers `409` while any indexer is pinned to the
client, with `error` and an `indexers` array of their names. Disabling the client
with `PUT` is not refused. A Prowlarr sync, and any other update from outside,
keeps the pin.

#### Per-indexer seed limits

Three optional overrides are applied to a torrent grabbed from the indexer:

| Field | Meaning | Accepted values |
|---|---|---|
| `seedRatio` | stop seeding at this upload ratio | a ratio, or `-1` for unlimited |
| `seedTimeMinutes` | stop seeding after this many minutes in total (#2206) | 1 to 5256000 |
| `inactiveSeedTimeMinutes` | stop seeding after this many minutes without upload (#2206) | 1 to 5256000 |

Omitted on `PUT` keeps the stored value, and `null` clears it so the download
client's own rule applies. A seed time below 1 or above 5256000 (ten years) is
rejected with 400. qBittorrent applies all three; Transmission the ratio and the
inactive time; Deluge the ratio only; rTorrent none of them. A limit the client
cannot apply is logged at debug and skipped.

`seedRatioSource` and `seedTimeSource` are read only provenance: `prowlarr`
when a Prowlarr sync filled the value from that indexer's `seedRatio` or
`seedTime`, `user` when the value was sent on create or the indexer has been
updated with `PUT` since, and absent when unset. Anything a client sends in
either source field is ignored. A Prowlarr sync never overwrites a `user` value, including a value
cleared to `null`. Prowlarr has no inactive seed time, so that field has no
source.

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

GET    /api/v1/queue                              downloads with live downloader overlay: every download except an `imported` one that finished more than 7 days ago.
                                                  failed, importFailed and importBlocked stay listed however old, since they wait for Retry,
                                                  Retry import, Match to book or Remove. Older imports are on the History page
       -> {"items":[..],"partial":true,"staleClients":[{"clientId":1,"name":"qBit","message":".."}]}
                                                  partial means a download client did not answer in time, so items is short
GET    /api/v1/queue/failed-count                 {"failed": n}: downloads in failed, importFailed or importBlocked, read from the database only (the nav badge)
POST   /api/v1/queue/grab                         submit a search result to the download client
                                                  a guid a recent search returned is grabbed from the server's
                                                  record (raw URL) for every caller; otherwise only the API key
                                                  uses the posted nzbUrl, and everyone else gets 400 "this search
                                                  result has expired, search again"
                                                  409 "already grabbed" when a row holds the guid; the body's
                                                  "forceAvailable":true means it is your own imported row and
                                                  the same grab with "force":true re-grabs it (reuses the row,
                                                  removes nothing from the client)
                                                  400 with "code":"no_download_client" when no enabled client
                                                  can take the release (the message names the protocol); 502
                                                  "failed to send the release to the download client" when the
                                                  client refused it or could not be reached (the cause is in
                                                  the log and on the queue row; a refused or timed out
                                                  connection adds Bindery's own hint); 500 for a storage fault.
                                                  /queue/{id}/retry answers the same way
POST   /api/v1/queue/{id}/retry-import           retry an importFailed/importBlocked item without re-downloading
POST   /api/v1/queue/{id}/retry                   re-send a failed item's release to the download client (no re-search)
POST   /api/v1/queue/bulk-retry                   retry many; {"ids":[..]}; per id {"ok":true,"action":"import"|"resend"}
DELETE /api/v1/queue/{id}                         remove (also from the download client, unless another queue item
                                                  still uses the same torrent/NZB: then only this row goes)
       ?deleteFiles=true                          have the client destroy the data too (same exception)
       ?removeFromClient=false                    forget Bindery's row only, leave the torrent/NZB in the client
POST   /api/v1/queue/bulk-delete                  remove many; {"ids":[..],"deleteFiles":false,"unmonitorBooks":false,"removeFromClient":true}

GET    /api/v1/pending                            grabs awaiting delay-profile clearance
POST   /api/v1/pending/{id}/grab                  promote pending to queue immediately; answers like
                                                  /queue/grab (400 "no_download_client", 502 send failure)

GET    /api/v1/queue/manual-import/lookup         parse + catalogue-match one path (admin)
GET    /api/v1/queue/manual-import/scan           enumerate + match book units under a folder (admin)
POST   /api/v1/queue/manual-import                import one path against a book (admin)
POST   /api/v1/queue/manual-import/batch          import selected {path, bookId} pairs (admin); audio files for
                                                    the same book import as one audiobook under one download id
POST   /api/v1/queue/manual-import/reassign       move a mis-matched file to another book (admin)
                                                    {"path","targetBookId","format"?,"relocate"?}: relocate false links the
                                                    file to the book and leaves it on disk as it is (200, done); true or
                                                    absent re-imports it, moving and renaming it (202, background) (#2055)
GET    /api/v1/queue/manual-import/reassign/preview  where that reassign would move and rename it (admin)
                                                    ?path=…&targetBookId=N[&format=ebook|audiobook]
POST   /api/v1/queue/manual-import/match          attach an importFailed download to a book and import its files (admin)

GET    /api/v1/reorganize/preview                 preview renaming tracked files to the current template (admin)
                                                    ?scope=book|author|library (&id=N for book/author)
POST   /api/v1/reorganize/apply                   move the selected files {fileIds:[…]} to their templated paths (admin)

GET    /api/v1/history                            grab / import / failure timeline
POST   /api/v1/history/{id}/blocklist             add the release to the blocklist; 422 when the event has no release GUID

GET    /api/v1/blocklist                          list blocked releases
DELETE /api/v1/blocklist/{id}                     remove an entry
DELETE /api/v1/blocklist/bulk                     bulk remove
```

#### Release URLs in responses carry no credentials

Search, queue, pending and grab responses strip every credential parameter
from `nzbUrl`, `guid` and `infoUrl`: the indexer `apikey`, `jackett_apikey`,
`passkey`, `torrent_pass`, `authkey`, `rsskey`, `tp`, `token`, `signature` and
the rest of the list in `internal/httpsec/redact.go`, plus `r` inside a
newznab `getnzb` link. Parameters separated by `;` count as well as `&`.
Credentials in the URL itself (`https://user:pass@host/...`) are replaced,
user name included. Magnets lose their `tr=` announce URLs and their `xs=` and
`as=` source URLs.

`nzbUrl` and `guid` also lose path segments shaped like a private tracker
passkey, RSS key or download token. A path is cut into runs of `A-Z a-z 0-9 _ -`
(compared after percent decoding), and a run is redacted when it is

| Shape | Example |
|-------|---------|
| hex of 16 or more characters with a letter and a digit | `/download/123/0a1b2c3d4e5f60718293a4b5c6d7e8f9/x.torrent` |
| 20 or more characters where one piece between `-` and `_` switches between letters and digits at least 4 times | `/torrent/download/12345.aB3dE5fG7hJ9kL2mN4pQ6rS8tU0vW1xY` |
| the value of a `passkey=` style pair in the path | `/rss/passkey=hunter2/feed.xml` |

Release names switch between letters and digits once or twice per piece
(`Stormlight4`, `Retail2010`, `128kbps`), so they are kept, as are numeric
ids, words, file names and non ASCII names. The segment after `/details/` or
`/getnzb/` is a newznab release id and is kept. Info hashes and UUIDs in a
download URL or GUID are redacted like any other token, which only costs
readability: grabs take the real URL from the server's record. A redacted run
becomes `REDACTED-` and 12 hex characters, a keyed hash that changes on every
restart, so two releases stay apart without revealing the value. Not covered:
keys shorter than these lengths, keys of only letters or only digits, a short
token with few digits (a random 20 character base62 key reaches 4
alternations about 77% of the time, a 32 character one about 96%), and URL
fragments.

`infoUrl` is a link people click, and detail pages are often addressed by a
hex id (an MD5, an info hash), so it keeps its path and only loses credential
parameters and `user:pass@`. When `infoUrl` comes from the item's `<link>`
(which torznab feeds also use as a download link, with or without an
enclosure) or equals the download URL or the GUID, it is redacted like them.

History event `data` and the log export get the same treatment as `nzbUrl`,
and the log export also decodes the tracker URLs inside a magnet before
redacting them.

A grab of a GUID a recent search returned uses the server's own record of the
raw URL, so post `guid` back exactly as the search returned it. The record
lives in memory for 24 hours; after a restart or expiry a session grab answers
`400` "this search result has expired, search again". An API key caller may
instead post a GUID the server has not seen along with its own raw `nzbUrl`,
which is grabbed as posted (with the indexer `apikey` added when the host
matches a configured indexer). Only the indexer `apikey` is put back: a
`jackett_apikey`, a passkey parameter, a passkey in the path or `user:pass@`
credentials that a response redacted cannot be recovered, so such a caller
must post the raw URL it got from the indexer, not one copied from a Bindery
response.

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

Both probes (`POST /api/v1/downloadclient/test` and
`POST /api/v1/downloadclient/{id}/test`) answer `400` for a host or URL Bindery
refuses to call, and `502` when the client was called and the probe failed. The
`error` of a `502` is never the raw failure text. A refused or timed out
connection names the client and the host and port you configured, then Bindery's
own hint for it (`could not reach SABnzbd at 192.168.1.5:8080 (connection
refused, check the port ...)`), with no URL in it; anything else is the first
line of what the client said, credentials redacted, at most 200 characters. The
full text is in the Bindery log.

`POST /api/v1/downloadclient/{id}/test` also reports whether Bindery can read the
client's completed folder, as `pathVisibility` with `status` `ok` or `warning`, the
remapped `path` Bindery checked and `remotePath`, the folder exactly as the client
reports it (the left half of a path remap). The field is absent for client types
that report no fixed completed folder. When `status` is `warning` for qBittorrent,
NZBGet or rTorrent, the response also carries `remapCandidates`, one entry per
folder Bindery could map it to: `path`, `source` (`ebookDownloadDir`,
`audiobookDownloadDir` or `library`, in that order) and a `before` and `after` sample
path. A folder identical on both sides is never offered, since a remap onto itself
changes nothing. When that leaves nothing, `remapNote` is `samePath` (mount the
storage into Bindery instead), and `unsupportedChars` when a path holds a comma or a
colon the remap syntax cannot carry. The candidates are read only. They contain no remap
string and nothing is saved; to apply one, send the normal `PUT
/api/v1/downloadclient/{id}` with the new `pathRemap`. Both fields are admin only
like the rest of the route, and `POST /api/v1/downloadclient/test` never carries
candidates.

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
* The folder checked is where a grab actually lands, worked out the way the grab itself is sent: the save path Bindery sends (rTorrent always, qBittorrent without a category, Transmission with an absolute category), the category save path (qBittorrent, with an empty one meaning the default save path plus the category name), the category folder (SABnzbd), the category DestDir or DestDir plus the category name when `AppendCategoryDir` is on (NZBGet), a Deluge label's move completed path, or the client default. `source` names which. The category is used exactly as a grab sends it: a category with spaces around it gives `client_path: warn`. A Deluge category is sent lowercased, because the Label plugin stores labels in lowercase and only accepts that form when labelling a torrent, so `Books` uses the `books` label's move completed path.
* For Deluge, `category` compares the configured categories, lowercased, with the labels the Label plugin lists, and says when it lowercases one. When the plugin is off it cannot list labels and `category` is `unknown`.
* Ebook and audiobook grabs are checked separately, because each resolves its own category and download folder. When both land in the same folder there is one `paths` row and the `client_path`, `remap` and `local_path` checks have no `mediaType`; otherwise each carries `mediaType` `ebook` or `audiobook`.
* `remapRule` is `client` (this client's path remap changed the path), `global` (`BINDERY_DOWNLOAD_PATH_REMAP` did) or `none`. A Windows drive path fails for a missing remap only when Bindery itself is not running on Windows. When no remap applies and the client's folder is not under any folder Bindery uses, `remap` is `warn` and `local_path` carries the failure and the fix. When Bindery sends the save path itself (rTorrent, qBittorrent without a category) it runs its download folder through the same remaps in reverse, the client's own first and `BINDERY_DOWNLOAD_PATH_REMAP` as the fallback, so `clientPath` is the folder the client is actually given. A client remap rule that covers the folder wins even when it maps the folder to itself (`/downloads:/downloads`), which is how a client opts out of a global remap meant for others. When the global remap produced the sent folder but the client's own default save folder is an absolute path on Bindery's own folder (that folder, inside it or above it) rather than on the sent one, `client_path` is `warn` and the fix names that identity remap. A relative default such as rTorrent's stock `./` is not checked.
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
GET    /api/v1/system/status                      version, commit, build date, newest published release, image cache size, Hardcover feature state; `hardcoverPausedUntil` while the daily quota holds, `hardcoverTokenRejected` while Hardcover has refused the saved token
POST   /api/v1/library/scan                       start a library scan in the background (202; {"queued": true} when one is already running)
GET    /api/v1/library/duplicate-candidates      read-only duplicate title groups across every author, paginated (#2999)
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
POST   /api/v1/system/db-integrity                on demand foreign key drift check, read only (admin)
POST   /api/v1/system/db-integrity/cleanup/preview  what a cleanup would do, per relationship, plus a confirmation token (admin)
POST   /api/v1/system/db-integrity/cleanup        back up, then clean up the previewed rows (admin)
GET    /api/v1/images?url=<encoded>               proxied + cached cover image (30-day TTL)
```

`POST /api/v1/system/db-integrity` runs `PRAGMA foreign_key_check` over the
whole database and groups what it finds by relationship (#3356). It is a full
scan, so it is a POST (a prefetcher or crawler cannot start one) and it only
ever runs when asked: never at boot, never on a timer. It reads and never
writes, and it returns counts only, no row ids and no row contents:

```json
{
  "relationships": [
    {"childTable": "book_files", "parentTable": "books", "onDelete": "CASCADE", "violations": 4}
  ],
  "total": 4,
  "checkedAt": "2026-10-18T12:00:00Z"
}
```

`onDelete` is the action the schema declares for that relationship (`CASCADE`,
`SET NULL`, `NO ACTION`, ...), which is what a cleanup would replay. A clean
database answers `relationships: []` and `total: 0`. A child table with two
foreign keys to the same parent (for example `requests` to `users`) gets one
row per key, told apart by `onDelete`.

The scan runs on its own short lived read only connection to the database
file, so it does not hold the connection the rest of Bindery uses: `/health`,
sign in and the UI keep answering while it runs. (With no database path, as in
tests on an in-memory database, it falls back to the shared connection and
does block it.) Only one check runs at a time: a second request while one is
running gets `409` straight away. A scan is stopped after 60 seconds with
`504`, and a client that disconnects stops it too. While it runs the scan
pins a WAL snapshot, which the 60 second limit bounds. Admin only, like
`/system/storage`. To preview the affected rows from the command line use
`bindery db-check`; the offline cleanup is `bindery db-repair` with the yes flag
(see [DEPLOYMENT.md](DEPLOYMENT.md#database-foreign-key-integrity)).

The cleanup (#3357) is two more routes, both admin only, both POST, both behind
the same CSRF guards as every other write:

`POST /api/v1/system/db-integrity/cleanup/preview` scans exactly as above and
adds what a cleanup would do with each relationship, plus a `previewToken`. It
changes nothing.

```json
{
  "relationships": [
    {"childTable": "book_files", "parentTable": "books", "onDelete": "CASCADE", "violations": 4, "action": "delete"},
    {"childTable": "downloads", "parentTable": "books", "onDelete": "SET NULL", "violations": 2, "action": "clear"},
    {"childTable": "authors", "parentTable": "users", "onDelete": "NO ACTION", "violations": 1, "action": "keep"}
  ],
  "total": 7, "fixable": 6, "unfixable": 1,
  "checkedAt": "2026-10-19T12:00:00Z",
  "previewToken": "1792410000.<digest>.<signature>",
  "expiresAt": "2026-10-19T12:15:00Z"
}
```

`action` is `delete` (`ON DELETE CASCADE`: the rows are removed), `clear`
(`ON DELETE SET NULL`: the reference is cleared and the row kept) or `keep`
(anything else: reported and left alone). Each relationship also carries
`fixable`, how many of its `violations` a cleanup can really resolve. Rows it
cannot address (a `WITHOUT ROWID` table) or cannot confirm as orphaned with the
repair's own test, or whose `SET NULL` column must have a value, are counted in
`violations` but not in `fixable`. The preview cannot cheaply know when another
table restricts a delete, so `fixable` can still overstate there; such rows show
up as skipped afterwards. A schema with `RAISE(ROLLBACK)` triggers is not
supported by the cleanup. A
relationship with none repairable has `action` `skipped`. `fixable` and
`unfixable` at the top level are the sums. `previewToken` is absent when nothing
is fixable. It is signed with a key that exists only in the running process, so
it expires when Bindery restarts, and after 15 minutes either way.

`POST /api/v1/system/db-integrity/cleanup` takes
`{"confirm": true, "previewToken": "..."}` and refuses to do anything without
both: a missing or false `confirm`, or a missing or forged token, is `400`, and
an expired token is `409`. It then scans again and compares the result with the
preview: if the numbers moved, the answer is `409` (`the database changed since
the preview`) and a fresh preview is needed. Only one check, preview or cleanup
runs at a time, and a second request gets `409` straight away. Then, in order:

1. A backup of the database is written to the `backups` folder under
   `BINDERY_DATA_DIR`, the same snapshot the backup endpoint takes. If it
   cannot be written the answer is `500` and nothing is repaired.
2. The rows are resolved the way `bindery db-repair` resolves them (the two share
   one implementation), in transactions of 500 rows, so the rest of Bindery keeps
   answering between batches.
3. A fresh scan reports what is left.

```json
{
  "deleted": 4, "cleared": 2, "skipped": 1,
  "skipNotes": ["authors -> users: ON DELETE NO ACTION, no safe automatic repair"],
  "remaining": [{"childTable": "authors", "parentTable": "users", "onDelete": "NO ACTION", "violations": 1, "action": "keep"}],
  "remainingTotal": 1,
  "verified": true,
  "backupPath": "/config/backups/bindery_20261019_120000_pre-fk-cleanup.db",
  "backupName": "bindery_20261019_120000_pre-fk-cleanup.db",
  "durationMs": 1200,
  "completedAt": "2026-10-19T12:00:05Z"
}
```

A row the database refuses to repair (a `NOT NULL` column that clearing would
empty, a row something else still restricts) is skipped with a note in
`skipNotes` and left as it is; it does not stop the rest of the cleanup, and a
rerun does not fail on it. Whatever `remaining` lists after a cleanup was not
repaired, so a relationship with a `CASCADE` or `SET NULL` rule shows `action`
`skipped` there, and one with no rule shows `keep`.

`verified` is false when the scan afterwards could not run, in which case
`remaining` means nothing. A failure after the backup (`500`, or `504` when the
15 minute limit is hit) answers with `backupPath` so the backup can be found; the
body never carries the underlying error, which goes to the log. A client that
disconnects stops the cleanup at the next batch boundary and is logged at info
as `cancelled`, not as a failure. Each batch is all or
nothing but the cleanup as a whole is not: batches already committed stay, and
running the cleanup again carries on, because every row is checked to still be
an orphan before it is touched. Admins are named in the log (`audit: database
drift cleanup started` and `finished`, with the user id, the counts and the
backup name).

`POST /api/v1/library/scan` answers `202` with `{"message": "library scan
started"}` when it starts a scan. While a scan is already running it no longer
answers `409`: the request is queued and the answer is `202` with
`{"message": "library scan queued", "queued": true}`. When the running scan
finishes, one more scan runs, however many requests arrived meanwhile, so a
file placed in a folder the walk had already passed is still picked up (#3014).
Both answers carry `scanId`, the `scan_id` the requested scan's result will
have in `GET /api/v1/library/scan/status`, so a client can recognise its own
scan's result. The status also carries `running` and `queued`, the live state
next to the stored result of the last finished scan.

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
| `reason` | `author_not_in_library`, `no_candidate_books`, `no_title_match`, `no_title_parsed`, `too_small` |
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

`rootFormat` is the format the row's library root holds, `ebook` or
`audiobook`, and is present only when `BINDERY_AUDIOBOOK_DIR` is a separate
folder from the library; with one combined root it is omitted. A row whose
`format` differs from it, such as an ebook under the audiobooks root, is in
the other format's folder (#2944). A `too_small` row is an ebook format file
under 4 KiB, too small to be a book: it is its own row and carries no
`candidates`.

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
| `400` | neither or both of `bookId` and `foreignBookId`, a format that is not the files' format, or an ebook file under 4 KiB, too small to be a book (Manual Import is the override for such a file) |
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
| `contentCheck` | on `bookImported` when `import.ebook.content_check` or `import.audiobook.content_check` is `warn` (the default) and the files look like another book, and on `downloadFailed` when `block` refused the download. Absent when the check found nothing worth recording and when it is `off`. Two notes ride without a flag and without any block: `retitled` (a download whose every file names one other title) and `truncated` (the check was skipped because it ran out of time, or a release held more EPUBs than the cap). See below |
| `requestCreated` extras | `kind` (`book` \| `author`), `username`, `mediaType`, `requestId`. `title`, `author` and `username` have control and invisible characters removed, are length capped, and have `@`, `<`, `>`, `[` and `]` replaced with fullwidth lookalikes, and the `://` of any URL broken the same way, so a title or username cannot mention a channel, use a Slack escape such as `<!channel>`, form a markdown link or autolink a URL. The same request from the same person notifies at most once an hour, and one requester at most 10 times an hour. The event is off on every webhook until its **Request** toggle is turned on |

**`contentCheck`** (#3015, #3246) says what the post-download content check found. The
same object rides on the `bookImported` history row's `data` and webhook in
`warn` mode, and on the `importFailed` history row's `data` and the
`downloadFailed` webhook in `block` mode. For an ebook:

```json
{
  "kind": "ebook",
  "bookTitle": "Mary, Mary: Alex Cross, Book 11",
  "bookAuthor": "James Patterson",
  "mismatches": [
    { "file": "book.epub", "title": "Mary and Mr Eliot: A Sort of Love Story", "author": "Mary Trevelyan" }
  ]
}
```

`mismatches` lists the ebook files (EPUB, or MOBI, AZW or AZW3 since #3016) that disagreed on both title and author, at most 10 of them, with `moreTitles` saying how many more there were and `filesRead` how many were opened (at most 20 per release). For an
audiobook (#3246) the object reports what the audio tags said:

```json
{
  "kind": "audiobook",
  "bookTitle": "Chase",
  "bookAuthor": "James Patterson",
  "tracksRead": 19,
  "matches": 2,
  "mismatches": [
    { "title": "Corralled", "count": 9 },
    { "title": "Coming Home to Myself", "count": 8 }
  ]
}
```

`tracksRead` is how many files had readable tags, `matches` how many of them
name the book, and `mismatches` the other works found with the number of files
each, most files first and capped at 10 titles (`moreTitles` says how many were
left out). `sampled: true` appears when the download held more than 500 audio
files and a sample spread across the folder was read. `retitled`
(`{title, files}`) appears on a pass where no file supports the book and every
file names one other title, with an empty `mismatches` list. `truncated: true`
means the 30 second budget ran out and the check failed open; `tracksRead` is how
far it got. The text is
cleaned like other text that comes from outside Bindery: control characters are
removed, length is capped, and `@`, `<`, `>`, `[`, `]` and the `://` of a URL are
replaced with fullwidth lookalikes, so a hostile file cannot mention a channel or
form a link in a chat relay. The settings that control it, `import.ebook.content_check`
and `import.audiobook.content_check`, take `off`, `warn` (the default) or `block`,
are admin only, and are listed by `GET /api/v1/settings/descriptors`.

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

### Calibre

```
POST   /api/v1/calibre/test                       probe calibredb or the Bindery Bridge plugin (admin)
POST   /api/v1/calibre/import                     start a library import from Calibre (admin)
GET    /api/v1/calibre/import/status              library import progress (admin)
POST   /api/v1/calibre/sync                       Push all: queue every eligible book for delivery (admin, plugin mode)
GET    /api/v1/calibre/sync/status                progress of the last Push all, read from the delivery queue (admin)
GET    /api/v1/calibre/deliveries/summary         queue counts, last delivery, whether Calibre was reachable (admin)
GET    /api/v1/calibre/deliveries                 queue rows with book title and author (admin; `?state=&limit=&offset=`)
POST   /api/v1/calibre/deliveries/retry           put failed or skipped rows back in the queue (admin)
DELETE /api/v1/calibre/deliveries?state=pending   drop every waiting row (admin)
POST   /api/v1/calibre/deliveries/reset           forget every delivery, for a new Calibre library (admin; needs {"confirm": true})
GET    /api/v1/book/{id}/calibre                  one book's delivery state (anyone who can see the book)
```

Every ebook Bindery sends to Calibre goes through a delivery queue (#2832),
one row per ebook file. A row is `pending` (waiting, or retrying after an
error), `delivered`, `failed` (gave up; only a retry puts it back) or
`skipped` (the book or file went away before it could be sent). Imports queue
their file, and a worker delivers every minute and straight after an import,
so a book imported while Calibre is closed goes out once Calibre is back. A
Calibre that cannot be reached is not counted as an attempt.

**Push all** (`POST /calibre/sync`) queues each imported, monitored book's
ebook file unless one of the book's ebook files is already in the queue, in
any state. A delivered book is never queued again and a failed one is not
rearmed; use retry for that. The response is `202` with the same progress
shape `GET /calibre/sync/status` returns: `running` stays true while any book
the run queued is still waiting, and the counts come from the queue
(`pushed` is newly added by Calibre, `alreadyInCalibre` includes books
delivered before the run, `failed` holds failures with their error, `skipped`
holds books the run left out and why). It still needs plugin mode: calibredb
has no "already in the library" answer, so a bulk run there would turn every
book the library already holds into a failure. A second Push all while the
first is still queueing is a `409`.

`GET /calibre/deliveries/summary`:

```json
{
  "pending": 3, "delivered": 412, "failed": 1, "skipped": 0,
  "lastDeliveredAt": "2026-09-27T11:02:13Z",
  "mode": "plugin",
  "target": {
    "lastPassAt": "2026-09-27T12:01:00Z",
    "checkedAt": "2026-09-27T12:01:00Z",
    "reachable": false,
    "lastError": "Get \"http://calibre:8099/v1/health\": dial tcp: connection refused"
  },
  "transport": "push",
  "pull": {}
}
```

`target` is what the worker last learned. It only refreshes while something is
waiting, so with an empty queue `checkedAt` can be old.

`transport` is `calibre.plugin_transport` (`push` or `pull`). In pull the
worker never contacts Calibre, so `target` stays empty and `pull` says when
the plugin last reached the bridge routes: `lastSeen`, `pluginVersion`,
`capabilities`, `remoteAddr`, and `library`, the Calibre library it last
acknowledged a delivery into. It is kept in memory, so it is empty after a
restart until the plugin checks in again.

`GET /calibre/deliveries` returns `{items, total}`. `state` is `pending`,
`delivered`, `failed`, `skipped` or empty for all; `limit` defaults to 50 and
caps at 500. Each item is the queue row (`filePath`, `format`, `state`,
`outcome`, `attempts`, `lastError`, `lastErrorCode`, `calibreId`, `updatedAt`,
`deliveredAt`) plus `bookTitle` and `authorName`.

`POST /calibre/deliveries/retry` takes `{"state": "failed"}`, `"skipped"` or
`""` for both, resets their attempts and returns `{"requeued": N}`. `DELETE
/calibre/deliveries` only accepts `state=pending` and returns `{"cleared": N}`;
the record of what was delivered is never cleared this way, because it is what
stops a book being sent twice. `POST /calibre/deliveries/reset` deletes every
row and returns `{"removed": N}`. It is for pointing Bindery at a different
Calibre library, where the old records would claim books are in a library
they never reached. Nothing is sent to the new library until Push all or an
import queues it.

`GET /book/{id}/calibre` answers `{"state": ...}` with `off` when the
integration is off, `none` when the book was never queued, or the state of
the row that speaks for the book (delivered first, then pending, failed,
skipped). Admins also get `outcome`, `lastError`, `lastErrorCode`,
`attempts`, `calibreId` and `deliveredAt`; other users get the state only,
since the error text can name server paths. A book the caller cannot see is a
`404`, as with `GET /book/{id}`.

### Calibre bridge (pull)

With `calibre.mode` set to `plugin` and `calibre.plugin_transport` set to
`pull` (#2833), the Calibre Bridge plugin (0.8.0 or later) connects out to
Bindery instead of Bindery connecting to it. The plugin lists the due
deliveries, downloads each file, adds it to Calibre and acknowledges it. The
push worker stands down in pull, so a book is never sent both ways.

```
GET    /bridge/v1/hello                    Bindery version, protocol, page size, transport
GET    /bridge/v1/deliveries               due deliveries (`?limit=&cursor=`)
GET    /bridge/v1/deliveries/{id}/file     the book file
GET    /bridge/v1/deliveries/{id}/cover    the cover image
POST   /bridge/v1/deliveries/{id}/ack      the plugin added it
POST   /bridge/v1/deliveries/{id}/nack     the plugin could not add it
```

These routes sit at the root like `/opds`, outside `/api/v1`, and under
`BINDERY_URL_BASE` when one is set.

**Authentication.** Every route needs `Authorization: Bearer <key>`, where the
key is `calibre.plugin_api_key`, the same key push mode sends to the plugin.
It is required in every auth mode, including Disabled and Local only. The
global API key, `X-Api-Key`, `?apikey=` and session cookies are not accepted
here, and the plugin key is accepted nowhere else. A stored key that is empty
or shorter than 16 characters refuses every request. Failed attempts are
counted per client address on a limiter of their own, separate from the
login limiter, so a plugin with a stale key cannot lock the admin out of the
web UI; past the limit the answer is `429` with `Retry-After`.

The plugin sends `X-Bridge-Version: <plugin version>` and
`X-Bridge-Capabilities: <comma separated>` (for example
`book_metadata,cover,add_format`) on every request. Bindery records the last
contact for the settings page.

Errors are JSON `{"error": "...", "code": "..."}`:

| Status | code | When |
|---|---|---|
| 400 | `invalid_request` | bad id, limit, cursor or body |
| 401 | `unauthorized` | missing or wrong key, or no usable key stored |
| 403 | `path_forbidden` | the delivery's file is outside the library roots, or not a regular file |
| 404 | `not_found` | no such pending delivery, the file is gone, or no cover |
| 409 | `not_in_pull_mode` | a delivery route while mode is not `plugin` or transport is not `pull` |
| 409 | `not_pending` | ack or nack of a row that already failed, was skipped, or was delivered to another Calibre id |
| 429 | `rate_limited` | too many failed attempts from this address |
| 500 | `internal` | a store or filesystem fault; the detail is in the server log, never in the body |

`GET /bridge/v1/hello` answers in push too, so the plugin can report that
Bindery is not in pull mode:

```json
{"binderyVersion": "v1.39.0", "protocol": 1, "maxBatch": 20, "transport": "pull"}
```

`GET /bridge/v1/deliveries` returns the pending rows that are due, a page at
a time. `limit` defaults to 20 and caps at 50; `cursor` is opaque, and an
empty `nextCursor` means there is nothing further. `pending` counts every due
row, not just this page.

```json
{
  "deliveries": [
    {"id": 812, "bookId": 97, "format": "epub", "sizeBytes": 482113,
     "action": "add", "hasCover": true,
     "metadata": {"title": "Emma", "authors": ["Jane Austen"],
                  "identifiers": {"bindery": "97", "isbn": "9780141439587"}}}
  ],
  "nextCursor": "97",
  "pending": 3
}
```

`metadata` is the object push mode sends in `POST /v1/books`, without
`coverPath`. Pages hold whole books and list each book's preferred format
first (epub, kepub, azw3, mobi, pdf, then the rest), like the push worker.
`action` is `add` for the first file of a book, and `add_format` for a file of
a book that already has a delivered file. A book's other files are held back
until its first is acknowledged, then listed as `add_format`. A plugin that
does not advertise `add_format` is never offered one: those rows are skipped
with the same reason push uses, and put back in the queue the next time the
plugin lists with `add_format` advertised.

`GET /bridge/v1/deliveries/{id}/file` streams the file recorded on the row
with `Content-Type: application/octet-stream`, `Content-Length` and
`Content-Disposition: attachment; filename="book.<ext>"`. Range requests
work. The path must be inside a library root, the same allow list as the
download route, and must be a regular file. A file that has gone is a `404`
and the row is skipped. Only pending rows are served.

`GET /bridge/v1/deliveries/{id}/cover` returns the cover image with its
content type, or `404`.

`POST /bridge/v1/deliveries/{id}/ack` takes
`{"calibreId": 1234, "outcome": "added" | "already" | "format_added",
"coverApplied": true, "library": "C:\\Users\\me\\Calibre Library"}` and
answers `204`. The row is marked delivered against the reported library, and
`books.calibre_id` follows the push rule: it is filled only when the book has
none, did not come from a Calibre import, and either no library path is set
or the library is that one. Sending the same ack again is a `204`.

`POST /bridge/v1/deliveries/{id}/nack` takes `{"code": "calibre_busy",
"error": "database is locked", "retryable": true}` and answers `204`. It
counts one attempt. `retryable: false`, or a `bad_format` or
`path_forbidden` code, fails the row for good; otherwise it backs off on the
push schedule (1 minute, 5 minutes, 15 minutes, 1 hour, 6 hours, 24 hours)
and gives up after 8 attempts.

The delivery queue is not per user: it is the install's one Calibre target,
and every route here is as privileged as an admin reading the queue.

### Readarr and CSV import (admin)

```
POST   /api/v1/migrate/readarr                    upload readarr.db (multipart "file"); 202 with the status below, 409 if one is running
GET    /api/v1/migrate/readarr/status             poll the import and its catalogue backfill
POST   /api/v1/migrate/csv                        upload a CSV of author names; answers when the names are added
```

The Readarr import runs in the background, in two phases. First the database
import (`running`), then a catalogue backfill that fetches each added author's
books at about 3 seconds per author, two at a time at most. The backfill
outlives the import, so `running` goes `false` while `catalogueRunning` is still
`true`. A client that wants the whole job keeps polling until both are `false`.

```json
{
  "running": false,
  "startedAt": "2026-10-18T12:00:00Z",
  "finishedAt": "2026-10-18T12:01:10Z",
  "message": "import complete",
  "error": "",
  "result": { "authors": { "requested": 500, "added": 480 }, "indexers": {}, "downloadClients": {}, "blocklist": {} },
  "catalogueRunning": true,
  "catalogueTotal": 480,
  "catalogueDone": 112
}
```

`message`, `error`, `finishedAt` and `result` are left out when empty. The three
`catalogue*` fields are always present. A failed fetch counts as done, so
`catalogueDone` reaches `catalogueTotal`. The counters stay readable after the
backfill ends, reset when the next import starts (unless a backfill is still
running, in which case the new authors are added to its total), and live in
memory only: a restart sets them back to `false`, `0`, `0`. An older server
leaves them out, which clients should read as no backfill.

The CSV upload has no status endpoint. Its catalogue backfill runs the same way
but is not reported.

### Settings

```
GET    /api/v1/setting                            list stored settings
GET    /api/v1/setting/{key}                      read one stored setting
PUT    /api/v1/setting/{key}                      write one setting (admin), body {"value": "..."}
DELETE /api/v1/setting/{key}                      delete one setting (admin)
GET    /api/v1/settings/descriptors               describe every key Bindery knows (admin)
```

Secrets (`*.api_key`, `*.api_token`, `auth.*`, and the rest of
`isSecretSetting`) never appear in a list or a read, not even for an admin.

Every other key is returned to admins only, with a short allowlist of keys a
non admin screen reads (#2361). A non admin caller gets exactly these, and any
other key is left out of the list and answers `404` from a read, the same as an
unset key:

| Key | Read by |
|-----|---------|
| `recommendations.enabled` | Discover page |
| `metadata.primary_provider` | add to library dialog |
| `library.defaultRootFolderId` | add author dialog |
| `library.defaultAudiobookRootFolderId` | add author dialog |
| `default.media_type` | add author dialog |
| `author.default_monitor_mode` | add author dialog |
| `author.default_monitor_latest_count` | add author dialog |

`metadata.primary_provider` accepts `openlibrary` (the default), `dnb`, `nb`
(Nasjonalbiblioteket), `bnf` (Bibliothèque nationale de France, #3144) and
`hardcover`. `nb` and `bnf` are queried only while selected: an instance that
never selects them never contacts either library.

`metadata.enrichers_disabled` (#1890, #2015, admin only) is a comma separated
list of sources to turn off, empty by default. It accepts `openlibrary`,
`hardcover`, `googlebooks` and `dnb`, which are the enrichers, plus `audible`
and `audnex`, the two audiobook sources. Names are trimmed, lowercased and
deduplicated on write, and a name outside that list is refused with `400`. The
primary provider is never turned off: naming it is ignored for as long as it is
primary. `nb` and `bnf` are not accepted, since they are only ever queried as
primary. A disabled source is sent no request. Turning off DNB also drops its
cover lookup, and turning off Hardcover removes it as an enricher only; the
separate enhanced Hardcover series setting is unchanged.

A `PUT` (or `DELETE`) of `metadata.primary_provider`, `metadata.enrichers_disabled`,
`googlebooks.apiKey`, `google_books_api_key`, `hardcover.api_token` or
`metadata.author_enrichment.wikidata` (#3287) rebuilds the metadata providers
before it answers, so the next lookup already uses the new set, with no restart
(#1771). The `PUT` response for those keys carries an extra boolean:

```json
{"key": "metadata.primary_provider", "value": "bnf", "updatedAt": "...", "appliedLive": true}
```

`appliedLive: false` means the value was stored but the providers could not be
rebuilt; the previous providers stay in use and the new value applies at the
next restart. Every other key answers without the field.

Requests authenticated with the API key are treated as admin, so a script or a
third party client using the key still reads every non secret setting. A client
signed in as a `user` account that read other keys should switch to the API
key. `adminOnly` in the descriptors below says which side of the line a key is
on.

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
POST   /api/v1/auth/logout                        revoke this session server side and clear the cookie
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
PUT    /api/v1/auth/users/{id}/role               change role (admin), body {"role": "admin"|"user"|"requester"}; 404 for an unknown user
PUT    /api/v1/auth/users/{id}/auto-approve       per-account request auto-approval (admin), body {"enabled": true|false}
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
