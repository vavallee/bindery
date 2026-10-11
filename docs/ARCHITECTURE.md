# Architecture

Bindery is a single Go binary with the React frontend embedded via `go:embed`. There are no sidecars, no separate static-file host, no external database. The runtime surface is one HTTP listener and one SQLite file.

## Data flow

```
   Newznab / Torznab
      indexers
         │
         ▼
┌────────────────────────────┐
│         Bindery            │──► SABnzbd / NZBGet / qBittorrent / Transmission / Deluge / rTorrent
│  Go backend + React SPA    │──► /books/ library  (and optional /audiobooks/ root)
│  SQLite (WAL mode)         │──► Webhook notifications
└────────────────────────────┘
    ▲                    ▲                    ▲
    │                    │                    │
OpenLibrary    Google Books, Hardcover.app,   Audnex, Audible
(default        DNB (enrichers)         (audiobook enrichment)
 primary)
```

Exactly one provider is *primary* — it defines what an author's catalogue is.
`metadata.primary_provider` selects OpenLibrary (default), DNB, Nasjonalbiblioteket
(`nb`), or Hardcover (token required); every provider that isn't primary is wired
as an enricher, except Nasjonalbiblioteket, which is wired only when it is primary.
Switching the primary away from it therefore leaves `nb:` authors with no provider
to sync from until they are relinked.

## Components

| Layer | Stack | Notes |
|-------|-------|-------|
| **HTTP router** | [chi](https://github.com/go-chi/chi) v5 | Sub-routers per resource, middleware-driven auth/CSRF/rate-limit. |
| **Backend language** | Go 1.26 (built with `golang:1.26.9-alpine`, the toolchain pinned in `go.mod`) | Standard library HTTP server, structured logging via `slog`. |
| **Database** | SQLite, WAL mode | [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — pure Go, no CGO. Single `bindery.db` file. Connection pragmas (`foreign_keys`, `busy_timeout`, `synchronous`, `temp_store`, `cache_size`) are carried in the DSN so the driver reapplies them to every connection it opens; see [Database durability](DEPLOYMENT.md#database-durability). |
| **Schema migrations** | Embedded SQL files in `internal/db/migrations/` | Linearly-numbered, additive-only, applied at startup. |
| **Frontend** | React 19 + TypeScript + Tailwind CSS 4 | Built with [Vite](https://vite.dev), output baked into the binary via `go:embed`. |
| **Container** | Multi-stage build on [distroless/static-debian13:nonroot](https://github.com/GoogleContainerTools/distroless) | No shell, no package manager, runs as UID `65532`. |
| **Helm chart** | `charts/bindery/` | ArgoCD- and Flux-friendly; supports `existingSecret`, NFS volumes, ingress. |

## Internal packages

The `internal/` tree is organised by domain, not by layer:

| Package | Responsibility |
|---------|----------------|
| `api` | HTTP handlers, request/response types, integration with `auth` middleware. |
| `auth` | Local accounts (argon2id), API keys, CSRF, sessions, OIDC, forward-auth, rate limiting. |
| `db` | Connection pooling, transaction helpers, repository interfaces, and the embedded schema migrations under `db/migrations/` (`NNN_description.sql`, applied idempotently at boot). |
| `migrate` | Bulk-import of authors and related records from a `readarr.db` or a Goodreads CSV export. |
| `models` | Domain types (Author, Book, Edition, Series, Indexer, etc.) shared across handlers, repos, and pipelines. |
| `metadata` | OpenLibrary, Google Books, Hardcover, DNB, Nasjonalbiblioteket, Audnex, Audible: fetchers and unifying interfaces. The HTTP providers share one request loop (`metadata/providerhttp`): a 429, 502, 503, 504 or transport error is retried up to 3 times, `Retry-After` is honoured up to 30 seconds with jittered backoff otherwise, and a refusal holds every request that client makes, failing one at once when the hold would outlast its deadline, reported as rate limited after a 429 and as unavailable after a 5xx. A Google Books daily quota refusal (`dailyLimitExceeded`, `quotaExceeded`) is not retried. The CSV, Readarr and Goodreads importers wait out a primary's hold and ask again (up to 3 times per lookup, and at most 10 minutes of waiting per run) instead of failing the row. A lookup still refused after that counts toward their primary outage breaker, so a primary that keeps refusing stops the import after 3 such lookups and the remaining rows fail with a "try again later" reason. The Add Book ISBN and ASIN lookups run under one 20 second deadline. Hardcover paces itself adaptively instead, and treats a 200 whose GraphQL errors report a rate limit as a refusal ([#2369](https://github.com/vavallee/bindery/issues/2369), [#2791](https://github.com/vavallee/bindery/issues/2791)). |
| `indexer` | Newznab/Torznab clients, query builder, four-tier fallback, per-indexer query deduplication, result deduplication, ranking. |
| `decision` | Quality profiles, language filter, custom formats, delay profiles, blocklist consultation. |
| `downloader` | SABnzbd, NZBGet, qBittorrent, Transmission, Deluge, rTorrent clients (queue/history polling, submission, deletion). |
| `importer` | NZO-ID matching, Move/Copy/Hardlink semantics, naming-token expansion, cross-FS-safe moves. |
| `scheduler` | Cron loops for auto-grab, refresh, release discovery, recommendations, cleanup. See [Scheduled jobs](#scheduled-jobs). |
| `recommender` | Discover engine — taste profile, candidate filters, multi-source signals. |
| `seriesmatch` | Series title and position matching shared by Audiobookshelf import and manual series linking: name normalisation, a first-party WRatio-style title score, and volume-number comparison. |
| `textutil` | The character-level folds every string comparison shares and the reasons they differ (see [search-design.md](search-design.md)), plus author-name, alias and description cleanup and Jaro-Winkler similarity. |
| `covers` | On-disk store for cover images Bindery owns rather than fetches, addressed as `bindery-cover:<sha256>` and served through the image proxy. |
| `jobs` | Tracker for detached background goroutines so the process drains them on shutdown before closing the database. |
| `normdrift` | No production code: property tests asserting the folds above agree where they must and differ where they should. |
| `calibre` | `calibredb` CLI integration, plugin-bridge HTTP client, `metadata.db` direct ingest. |
| `abs` | Audiobookshelf import — runs, provenance, conflicts, review queue. |
| `prowlarr` | Prowlarr server registration and indexer sync. |
| `opds` | OPDS 1.2 catalogue feeds (root, recent, by author, by series, book entry/file). |
| `notifier` | Webhook sender (SSRF-guarded), retry policy, test-fire endpoint. |
| `httpsec` | Outbound URL guards (SSRF, DNS-rebinding), inbound header hardening (CSP, HSTS, frame-deny). |
| `telemetry` | Once-daily anonymous ping: version, feature counts, coarse error-class counters (opt-out via setting or env var). |
| `logbuf` | Persistent log buffer backing the in-app Settings → Logs viewer. |
| `webui` | The `go:embed` filesystem for the built React SPA, with URL-base rewriting. |
| `config` | Loads bootstrap runtime configuration from environment variables (paths, ports, retention, feature flags). |
| `bookhydrate` | Persists metadata-provider edition details for newly created or rebound books. |
| `concurrency` | Small primitives for bounding goroutine fan-out in handlers and background jobs. |
| `grimmory` | Grimmory self-hosted digital-library integration: JWT/token client, post-import BookDrop pusher, and bulk sync job (Settings exposes config/test/sync routes under `/grimmory/*`). |
| `hardcoverlistsyncer` | Syncs Hardcover reading lists into the catalogue as "wanted" books. |
| `isbnutil` | Normalizes ISBN inputs for metadata-provider lookups. |
| `metrics` | Prometheus exposition-format runtime metrics (registry, instances, HTTP handler). |
| `pathmap` | Rewrites paths between external-service mount points and Bindery-visible mount points. |
| `useragent` | Produces the canonical `User-Agent` string sent on every outbound HTTP request. |

## Storage layout

A typical container has three logical mounts:

| Mount | Purpose | Default |
|-------|---------|---------|
| `/config` | SQLite database, backups, image cache, owned cover store (`covers/`), Calibre cover cache (`calibre-covers/`), cookie/CSRF secrets | `BINDERY_DATA_DIR`, `BINDERY_DB_PATH` |
| `/books` | Imported ebook library (and audiobooks unless split out) | `BINDERY_LIBRARY_DIR` |
| `/downloads` | Where the download client deposits completed jobs | `BINDERY_DOWNLOAD_DIR` |

If audiobooks live on a different volume, set `BINDERY_AUDIOBOOK_DIR` (and optionally `BINDERY_AUDIOBOOK_DOWNLOAD_DIR`). When Bindery and the download client mount the same storage at different paths (common in Kubernetes), use `BINDERY_DOWNLOAD_PATH_REMAP` — see [DEPLOYMENT.md](DEPLOYMENT.md#path-remapping-multi-container--multi-pod-setups).

## Concurrency model

- One HTTP server goroutine pool; chi's per-request handlers run on caller goroutines.
- Background workers (auto-grab sweep, recommendations refresh, indexer probes, ABS import) are scheduled by the `scheduler` package as long-lived goroutines guarded by context cancellation on shutdown.
- SQLite runs in WAL mode, but the connection pool is pinned to a single connection (`SetMaxOpenConns(1)`), so **reads serialize alongside writes** rather than running concurrently. WAL's concurrent-reader property is not currently being used. This is sufficient for the workload in practice, and [#2147](https://github.com/vavallee/bindery/issues/2147) tracks lifting it, including the reason it is not a one-line change: migrations run a connection-scoped `PRAGMA foreign_keys=OFF`, which a pool would break.
- All outbound HTTP calls go through a shared client with timeouts, SSRF guards, and User-Agent stamping (`bindery/<version>`).

## Scheduled jobs

Registered by `internal/scheduler`. Every job runs under `SkipIfStillRunning`, so a run that overruns its interval skips the next one rather than queueing behind it, and each run is recorded through `metrics.ObserveSchedulerRun`.

| Job | Interval | What it does |
|-----|----------|--------------|
| `check-downloads` | 15s | Polls download clients and imports finished jobs. A job NZBGet or SABnzbd failed for its content (a failed repair or unpack, missing articles, a broken NZB) also blocklists the release, so the next search picks another. NZBGet's `FAILURE/UNPACK`, `FAILURE/PAR` and `FAILURE/SCAN` can also mean a fault on NZBGet's own machine, so for those the job log (`loadlog`) is read first, an evidence free `FAILURE/UNPACK` asks NZBGet 24's `sysinfo` for a missing UnRAR (lazily, cached six hours, never from the periodic probe; `HealthStore.NZBGetUnpackers`), and with no evidence a per client, per step breaker pauses blocklisting after three different releases fail the same step within two hours (`importer.contentFailureBreaker`, reported on the client's health); any other failure is retried after `models.DeadRegrabCooldown` ([#3024](https://github.com/vavallee/bindery/issues/3024)). |
| `check-stalled` | 5m | Fails and re-searches downloads stuck past the stall timeout (`stall.timeout_minutes`, default 120). Stuck means either the client's own per torrent signal (qBittorrent stalledDL, a Transmission errorString, Deluge Error, an rTorrent message), which also blocklists the release, or a torrent the client accepted and never resolved the metadata for: no files, no size, no progress ([#2709](https://github.com/vavallee/bindery/issues/2709)). The second kind does **not** blocklist, because it is as much a property of the network as of the release and the blocklist has no expiry; its torrent is removed without deleting data (it has none), and its re-search skips that one release so it can pick another. It is also only applied past the timeout, because a healthy magnet looks the same while it resolves, and it is skipped entirely for a client where more than half the unfinished torrents look that way, which is a connectivity fault rather than a run of bad releases. |
| `download-client-health` | 15m | Re-probes download client reachability and paths. |
| `search-wanted` | `search.interval`, default 12h, read at startup | Searches indexers for wanted books and auto-grabs when enabled. |
| `refresh-metadata` | 24h | Refreshes four profile fields on monitored authors. Creates no books. |
| `author-discovery` | hourly tick, cadence from `authors.discovery.interval` (unset and `off` both disable it, which is how it ships; a stored 24h to 720h duration turns it on), read every tick | Runs the author catalogue sync for a batch of monitored authors whose `last_discovery_at` is older than the interval, never checked first. Batch is `ceil(eligible / hours in interval)`, clamped to 1..25, read with 3 spare authors so one whose sync is already running is skipped without costing the slot, with 3 seconds between authors and a 10 minute budget per author (an author over budget is stamped). Checks before every author whether Refresh all or refresh selected is running and stops if so. Stops on a rate limit from any provider (`metadata.ErrRateLimited`, the refused author unstamped), and after 3 consecutive authors failing with the provider unavailable (`metadata.IsProviderUnavailable`: rate limit, 5xx, network error, timeout); the authors of that streak are stamped to be due again in 6 hours (capped at the interval) so they cannot hold the head of the queue. Any other error is about the author, is stamped and resets the streak. Re-reads each author and skips one deleted, unmonitored or set to add no new items, before any provider call. Enriches covers only for works the author does not have, so existing coverless books get covers from a manual refresh only. Same author catalogue syncs serialise from reading the author's books to hydrating the created ones; the lock is released before indexer searches and the announcement, waiting for it ends with the context, and the AddBook single work fallback does not take it. Never grabs; new monitored books reach indexers through `search-wanted`. Publishes `bookAnnounced` ([#2236](https://github.com/vavallee/bindery/issues/2236)). |
| `scan-library` | 6h | Reconciles files under the library roots against the catalogue. |
| `calibre-sync` | 24h | Imports from a Calibre library, when configured. |
| `calibre-deliver` | 1m | Delivers queued ebook files to Calibre (`calibre_deliveries`). Does nothing with Calibre off or the queue empty; stops without counting an attempt when the bridge does not answer a 5 second health probe. Retries failures with backoff (1m, 5m, 15m, 1h, 6h, then 24h) and gives up after 8 attempts. Imports kick an extra pass; passes never overlap. |
| `recommendations` | 24h | Rebuilds Discover recommendations, when enabled. |
| `hardcover-sync` | `hardcover.sync_interval`, default 24h, read at startup | Syncs Hardcover import lists, when configured. |
| `telemetry-ping` | 24h | Anonymous install ping, unless opted out. |
| `log-trim` | 24h | Trims the persistent log store to its retention. |

## Why these choices

- **Single binary, embedded UI** — no nginx, no `static/` mount to forget about, no version-skew between API and UI.
- **Pure-Go SQLite** — no CGO means cross-compilation works for every release target (Linux amd64/arm64/armv7/armv6, macOS amd64/arm64, Windows amd64/arm64) without per-platform toolchains.
- **Distroless** — the container has no shell, no package manager, no network tools, no setuid binaries. Attack surface is the Bindery process and nothing else.
- **Stable public APIs only** for metadata — Bindery survives Goodreads outages, scraper bans, and cookie-wall changes because it never depended on any of them.
