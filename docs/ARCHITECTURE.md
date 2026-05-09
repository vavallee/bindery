# Architecture

Bindery is a single Go binary with the React frontend embedded via `go:embed`. There are no sidecars, no separate static-file host, no external database. The runtime surface is one HTTP listener and one SQLite file.

## Data flow

```
   Newznab / Torznab
      indexers
         │
         ▼
┌────────────────────────────┐
│         Bindery            │──► SABnzbd / NZBGet / qBittorrent / Transmission / Deluge
│  Go backend + React SPA    │──► /books/ library  (and optional /audiobooks/ root)
│  SQLite (WAL mode)         │──► Webhook notifications
└────────────────────────────┘
    ▲                    ▲                    ▲
    │                    │                    │
OpenLibrary    Google Books, Hardcover.app,   Audnex, Audible
 (primary)         DNB (enrichers)        (audiobook enrichment)
```

## Components

| Layer | Stack | Notes |
|-------|-------|-------|
| **HTTP router** | [chi](https://github.com/go-chi/chi) v5 | Sub-routers per resource, middleware-driven auth/CSRF/rate-limit. |
| **Backend language** | Go 1.25 | Standard library HTTP server, structured logging via `slog`. |
| **Database** | SQLite, WAL mode | [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — pure Go, no CGO. Single `bindery.db` file. |
| **Schema migrations** | Embedded SQL files in `internal/migrate` | Linearly-numbered, additive-only, applied at startup. |
| **Frontend** | React 19 + TypeScript + Tailwind CSS 3 | Built with [Vite](https://vite.dev), output baked into the binary via `go:embed`. |
| **Container** | Multi-stage build on [distroless/static-debian12:nonroot](https://github.com/GoogleContainerTools/distroless) | No shell, no package manager, runs as UID `65532`. |
| **Helm chart** | `charts/bindery/` | ArgoCD- and Flux-friendly; supports `existingSecret`, NFS volumes, ingress. |

## Internal packages

The `internal/` tree is organised by domain, not by layer:

| Package | Responsibility |
|---------|----------------|
| `api` | HTTP handlers, request/response types, integration with `auth` middleware. |
| `auth` | Local accounts (argon2id), API keys, CSRF, sessions, OIDC, forward-auth, rate limiting. |
| `db` | Connection pooling, transaction helpers, repository interfaces. |
| `migrate` | Embedded migrations (`NNN_description.sql`), applied idempotently at boot. |
| `models` | Domain types (Author, Book, Edition, Series, Indexer, etc.) shared across handlers, repos, and pipelines. |
| `metadata` | OpenLibrary, Google Books, Hardcover, DNB, Audnex, Audible — fetchers and unifying interfaces. |
| `indexer` | Newznab/Torznab clients, query builder, four-tier fallback, deduplication, ranking. |
| `decision` | Quality profiles, language filter, custom formats, delay profiles, blocklist consultation. |
| `downloader` | SABnzbd, NZBGet, qBittorrent, Transmission, Deluge clients (queue/history polling, submission, deletion). |
| `importer` | NZO-ID matching, Move/Copy/Hardlink semantics, naming-token expansion, cross-FS-safe moves. |
| `scheduler` | Cron loops for auto-grab, refresh, recommendations, cleanup. |
| `recommender` | Discover engine — taste profile, candidate filters, multi-source signals. |
| `seriesmatch` | Four-tier reconciliation (ASIN → title+author → series+position → fuzzy). |
| `calibre` | `calibredb` CLI integration, plugin-bridge HTTP client, `metadata.db` direct ingest. |
| `abs` | Audiobookshelf import — runs, provenance, conflicts, review queue. |
| `prowlarr` | Prowlarr server registration and indexer sync. |
| `opds` | OPDS 1.2 catalogue feeds (root, recent, by author, search). |
| `notifier` | Webhook sender (SSRF-guarded), retry policy, test-fire endpoint. |
| `httpsec` | Outbound URL guards (SSRF, DNS-rebinding), inbound header hardening (CSP, HSTS, frame-deny). |
| `telemetry` | Once-daily anonymous version ping (opt-out via setting or env var). |
| `logbuf` | Persistent log buffer backing the in-app Settings → Logs viewer. |
| `webui` | The `go:embed` filesystem for the built React SPA, with URL-base rewriting. |

## Storage layout

A typical container has three logical mounts:

| Mount | Purpose | Default |
|-------|---------|---------|
| `/config` | SQLite database, backups, image cache, cookie/CSRF secrets | `BINDERY_DATA_DIR`, `BINDERY_DB_PATH` |
| `/books` | Imported ebook library (and audiobooks unless split out) | `BINDERY_LIBRARY_DIR` |
| `/downloads` | Where the download client deposits completed jobs | `BINDERY_DOWNLOAD_DIR` |

If audiobooks live on a different volume, set `BINDERY_AUDIOBOOK_DIR` (and optionally `BINDERY_AUDIOBOOK_DOWNLOAD_DIR`). When Bindery and the download client mount the same storage at different paths (common in Kubernetes), use `BINDERY_DOWNLOAD_PATH_REMAP` — see [DEPLOYMENT.md](DEPLOYMENT.md#path-remapping-multi-container--multi-pod-setups).

## Concurrency model

- One HTTP server goroutine pool; chi's per-request handlers run on caller goroutines.
- Background workers (auto-grab sweep, recommendations refresh, indexer probes, ABS import) are scheduled by the `scheduler` package as long-lived goroutines guarded by context cancellation on shutdown.
- SQLite uses WAL mode with a single writer; reads are concurrent, writes serialize at the connection-pool layer. This is sufficient for the workload — measured contention is negligible on libraries with tens of thousands of books.
- All outbound HTTP calls go through a shared client with timeouts, SSRF guards, and User-Agent stamping (`bindery/<version>`).

## Why these choices

- **Single binary, embedded UI** — no nginx, no `static/` mount to forget about, no version-skew between API and UI.
- **Pure-Go SQLite** — no CGO means cross-compilation works for every release target (Linux amd64/arm64/armv7/armv6, macOS amd64/arm64, Windows amd64/arm64) without per-platform toolchains.
- **Distroless** — the container has no shell, no package manager, no network tools, no setuid binaries. Attack surface is the Bindery process and nothing else.
- **Stable public APIs only** for metadata — Bindery survives Goodreads outages, scraper bans, and cookie-wall changes because it never depended on any of them.
