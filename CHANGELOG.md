# Changelog

All notable changes to Bindery are documented here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com) and versions follow
[Semantic Versioning](https://semver.org).

## [Unreleased] — development branch

The `development` branch carries the in-flight feature set for the next release. Images are published as `ghcr.io/vavallee/bindery:development` and `:dev-<sha>`; point ArgoCD at the `development` branch to follow. Treat these features as beta — schema migrations are additive and safe, but UX may still shift before tagging.

### Authentication overhaul

Replaces the single-env-var API key gate with a full Sonarr-parity auth model. Upgrading from v0.5.x: the first launch after upgrade detects no user, redirects to `/setup`, and you create an admin account. `BINDERY_API_KEY` is still honoured as a seed for the new DB-stored key so existing integrations keep working on restart; after that it is inert (the key can be regenerated in-app).

#### Added
- **Password-based login** — first-run `/setup` flow creates a single administrator account. Passwords hashed with argon2id (OWASP 2024 parameters). Minimum 8 characters enforced client-side.
- **Signed session cookies** — self-contained HMAC-SHA256 cookies (no server-side session table). `bindery_session` is `HttpOnly` + `SameSite=Lax`. 30-day "remember me" or 12-hour default. `Secure` is intentionally left unset because TLS is usually terminated upstream (Traefik); front with a proxy that adds `Strict-Transport-Security` if you need strict HTTPS-only cookies.
- **Three auth modes** — `enabled` (always require login), `local-only` (bypass auth for RFC1918 / loopback / link-local / IPv6 ULA), `disabled` (no auth — only for trusted reverse-proxy deployments). Toggle in **Settings → General → Security**. Sonarr v4 parity.
- **Per-account API key** — auto-generated on first boot, visible/regenerable in the Settings Security panel. Accepts `X-Api-Key` header or `?apikey=` query param. Independent of the session cookie so scripts, `curl`, Tautulli, etc. work without cookies.
- **Login rate limiting** — per-IP sliding window, 5 failures / 15 minutes, returns `429`. Blocks credential-stuffing on internet-exposed deployments.
- **New endpoints** — `GET /auth/status`, `POST /auth/login`, `POST /auth/logout`, `POST /auth/setup`, `GET /auth/config`, `POST /auth/password`, `POST /auth/apikey/regenerate`, `PUT /auth/mode`.

#### Changed
- `/api/v1/*` is now authenticated by default (previously optional). Health, auth status/login/logout/setup, and the setup flow itself are exempt.
- `BINDERY_API_KEY` is now a **seed-only** bootstrap variable. If set on first boot, the generated key matches it; on subsequent boots the stored DB value wins. Setting the env var on an already-initialised instance has no effect.
- `auth.api_key`, `auth.session_secret`, and `auth.mode` settings are filtered out of the generic `GET /setting` and `GET /setting/{key}` endpoints — they are readable only via `/auth/config` for the authenticated admin.
- Frontend: added `/login` and `/setup` routes, an `AuthProvider` + `AuthGuard` that redirect unauthenticated users, a "Sign out" action in the header, and a Security section in Settings → General.

#### Fixed
- Middleware was treating `/auth/status` as an unauth-allowed path *before* verifying the session cookie, so the endpoint always reported `authenticated: false`. Valid logins still set the cookie correctly but the UI bounced right back to `/login`. Cookie verification now runs for every request; the unauth-allow list only controls the 401 rejection.
- Login and setup forms now read values via `FormData` on submit instead of relying on React-controlled state. Browser password-manager autofill populates `input.value` without firing `onChange`, which left React state empty and silently disabled the submit button.

### Author delete can sweep files ([#15](https://github.com/vavallee/bindery/issues/15))

`DELETE /api/v1/author/{id}?deleteFiles=true` now walks every book's `file_path` and removes it from disk before the DB cascade takes the rows out. Paths are collected *before* the delete (the cascade wipes the book rows that hold them, so a post-delete walk would find nothing). Per-path errors are logged but don't abort the response — the author is already gone and a partial sweep is better than rolling the whole thing back.

The UI confirm dialogs on the Author list and detail pages peek at each author's books, name the file/folder count in the confirmation message, and pass `deleteFiles=true` when the user OKs. Authors with no files on disk get the old plain confirm.

Closes the orphan-files gap reported against the Jared Diamond delete after #9 landed.

### Metadata language filter ([#14](https://github.com/vavallee/bindery/issues/14))

Foreign-language works from OpenLibrary/Hardcover/Google Books were landing in the library regardless of the user's preferred language. The `metadata_profiles` table (seeded in migration 003) already carried `allowed_languages='eng'` by default, but nothing consulted it — author-book ingestion filtered against a separate global `search.preferredLanguage` setting, and authors were never linked to a profile.

#### Added
- Author record now carries `metadata_profile_id`; `POST /author` and `PUT /author/{id}` accept `metadataProfileId`. New authors default to the seeded "Standard" profile (id=1) so the language filter applies out of the box.
- Metadata profile editor in **Settings → Metadata Profiles** — create/edit profiles with a language multi-select (English, French, German, Dutch, Spanish, Italian, Portuguese, Japanese, Chinese, Russian). Empty selection = accept any language.
- Metadata profile picker in the Add Author modal (shown when more than one profile exists).

#### Changed
- `FetchAuthorBooks` now filters against the author's metadata profile's `allowed_languages` CSV instead of the global `search.preferredLanguage` setting. Books with an unknown language are always kept (data-availability varies by provider).

#### Security notes
- Sessions use `SameSite=Lax`, which mitigates cross-site form-submission CSRF. An explicit CSRF token pass is on the roadmap.
- OIDC / SSO and reverse-proxy header trust are explicitly out of scope for this release; see the Roadmap in the README for the planned path.

## [v0.5.2] — 2026-04-13

### Security & hardening

Followed up v0.5.1 with a gosec audit pass. One HIGH-severity finding was real; the rest were false positives (taint analysis couldn't see input validation). Fixed the real issue and tightened two adjacent MEDIUM items.

#### Fixed
- **Remote filesystem deletion via book update (HIGH).** `PUT /api/v1/book/{id}` previously accepted a `filePath` field and wrote it to the book record unchecked. A caller could then trigger `DELETE /api/v1/book/{id}?deleteFiles=true` (or `DELETE /api/v1/book/{id}/file`) to run `os.RemoveAll` on that path — unbounded by the library dir. When `BINDERY_API_KEY` is unset (a warn-only configuration) this is unauthenticated. `filePath` is now omitted from the update schema; it remains internally-set by the importer after a successful grab.
- **Multipart upload error response.** `/api/v1/migrate/csv` and `/api/v1/migrate/readarr` already capped body size via `http.MaxBytesReader`, but passed `nil` as the ResponseWriter, so oversize requests surfaced as a generic 400. They now pass `w`, so oversize uploads receive a proper `413 Request Entity Too Large`.

#### Changed
- Backup directory (`<data>/backups`) is now created with mode `0700` instead of `0755`. SQLite snapshots there may contain indexer/client credentials in plaintext rows; only the bindery process should read them.
- Library and audiobook directories created by the importer are now `0750` (was `0755`). Host users needing read access should run bindery with a matching UID/GID (standard `PUID`/`PGID` pattern used by Readarr/Sonarr containers).

## [v0.5.1] — 2026-04-13

### Packaging & cross-platform

#### Fixed
- **Multi-arch Docker image.** `ghcr.io/vavallee/bindery` is now published as a multi-arch manifest covering `linux/amd64` and `linux/arm64`. Previously only `amd64` was built, so `docker compose pull` on Apple Silicon (and Raspberry Pi 4 / 5 running 64-bit Docker) failed with `no matching manifest for linux/arm64/v8 in the manifest list entries`. The Dockerfile now cross-compiles Go natively via buildx `TARGETOS` / `TARGETARCH` build args, so the arm64 variant builds on the amd64 runner without QEMU emulation overhead. Fixes #4.

#### Added
- **Pre-built release binaries** attached to every GitHub Release via GoReleaser. Targets: linux (amd64/arm64/armv7/armv6), macOS (amd64/arm64), windows (amd64/arm64). A `bindery_vX.Y.Z_checksums.txt` file is published alongside the archives for integrity verification. Raspberry Pi 4 / 5 use the `linux_arm64` archive; Pi 2 / 3 running 32-bit use `linux_armv7`; Pi Zero / Pi 1 use `linux_armv6`.

## [v0.5.0] — 2026-04-13

### Audiobook support + Readarr-parity UX + migration paths

### Import cleanup
- Ebook import no longer leaves the SABnzbd job folder behind. After every book file matches bindery's extension set and moves cleanly, the importer removes the source directory — PAR2, NFO, SFV, NZB, and sample leftovers go with it. Partial-failure runs are untouched so the files remain for investigation.
- Audiobook import handles destination collisions. `UniqueDir` resolves `{Author}/{Title} ({Year})` against the filesystem and appends ` (2)`, ` (3)`, … when a prior import or manual copy already occupies the slot. Previously `MoveDir` hard-failed on any collision and the download stuck at `Completed` forever.
- SABnzbd history is pruned once bindery owns the files. New `DeleteHistory(nzoID, deleteFiles=false)` on the SAB client is called after each successful import so completed rows stop accumulating in SAB's UI with stale storage paths.
- **Remote path mapping** (`BINDERY_DOWNLOAD_PATH_REMAP`). When SABnzbd and bindery run in separate containers with the shared storage mounted at different paths, SAB would report a completed job at `/downloads/complete/X` and bindery would fail to find it under its own mount point — logging `no book files found in download` and leaving files in SAB's completed dir forever. The new env var accepts comma-separated `from:to` pairs (e.g. `/downloads:/media`), applied longest-prefix-first to each path before the importer walks it. Same-filesystem installs leave it unset and see no behaviour change.

### Audiobook support
- Books now carry a `media_type` (`ebook` | `audiobook`) that drives indexer categories, ranking, library destination, and UI badges. Flip per-book inline on the Wanted page or via the Book detail page.
- Search pipeline: `filterCategoriesForMedia` narrows indexer queries to the Newznab audio tree (3030) for audiobook books and the books tree (7000 range) for ebooks, with a fallback to the standard category when the indexer's configured set has nothing matching.
- Ranking applies a −500 media-type-mismatch penalty and +250 for ASIN exact matches parsed from release titles. `isAudiobookFormat` recognises `m4b` / `m4a` / `mp3` / `flac` / `ogg`.
- Import pipeline: audiobook grabs move the entire download directory as one unit via `MoveDir` (multi-part `m4b` / `mp3` + cover art + cue sheet stay together) into `BINDERY_AUDIOBOOK_DIR` (falls back to `BINDERY_LIBRARY_DIR` if unset). Naming template defaults to `{Author}/{Title} ({Year})` — preserves original filenames inside.
- Audnex metadata provider (`api.audnex.us`, no auth) fetches narrator, duration, cover, and description by ASIN. Endpoint: `POST /api/v1/book/{id}/enrich-audiobook`.
- Release parser extracts Audible-shaped ASINs (`B[0-9A-Z]{9}`) from NZB titles; `UNABRIDGED` / `ABRIDGED` / `RETAIL` edition flags now factor into ranking.
- Raw per-article Usenet postings (`.part09.rar`, `.vol003+004.par2`, `.sfv`, `yEnc`, `[12/22]` brackets) filtered out of search results before ranking so multi-part noise no longer buries clean `[M4B]` releases.

### Readarr-parity UX
- **Book and author detail pages** at `/book/:id` and `/author/:id` — routed, deep-linkable, replace the previous click-opens-modal flow. Book detail hosts cover, metadata, format toggle, ASIN field, audnex enrich button, inline search-and-grab, and per-book history. Author detail shows portrait + stats + description + Monitored/Refresh/Delete actions + their books as a mini grid.
- **Grid / Table view toggle** on Books and Authors pages (persists per-page in localStorage). Books table: thumbnail + title, author, year, type, status with responsive column hiding on phones. Authors table: avatar + name, book count, rating, Monitored toggle, inline Refresh/Delete.
- History page adds **Size** and **Type** columns (desktop table + mobile card) — type auto-detected from the release title's format tokens.
- Books tab: audiobook corner badge on cards; inline `<select>` per row on Wanted persists media type via `PUT /api/v1/book/{id}`.

### Migration paths
- `POST /api/v1/migrate/csv` — upload a newline-separated list of author names or a `name,monitored,searchOnAdd` CSV. Each name resolved via OpenLibrary.
- `POST /api/v1/migrate/readarr` — upload `readarr.db`. Authors re-resolved via OpenLibrary (Goodreads IDs aren't portable since bookinfo.club is dead); Indexers / Download clients / Blocklist entries port structurally.
- `bindery migrate csv <path>` and `bindery migrate readarr <path>` CLI subcommands — exit with JSON summary.
- **Settings → Import** tab with file uploads and per-section result cards showing requested / added / skipped / failures.

### Infrastructure
- `development` branch joins `main` in CI — builds push `:development` + `:dev-<sha>` images and auto-bump `charts/bindery/values.yaml`. Point ArgoCD `targetRevision` at `development` to follow dev builds.
- Version badge shows `dev-<sha>` on development builds, `sha-<sha>` on main builds, or `v0.4.x` on tagged releases.
- File download endpoint (`/api/v1/book/{id}/file`) now streams a zip when the book's `file_path` is a directory (audiobook folders come down as a single archive).
- Background download-status poll tightened from 60s to 15s so imported status lands in near-real-time after SABnzbd finishes.
- Fixed a `rankResults` bug where precomputed scores were read from stale indices during the in-place sort — composite ranking effectively fell back to indexer-return order. Now zips score with result and sorts pairs. Regression test added.

### Added (smaller)
- `/book/{id}/enrich-audiobook` endpoint (audnex).
- Foreign-language tag filter now word-boundary-anchored (the tag `RUSSE` no longer substring-matches inside `RUSSELL`).
- Book PUT handler accepts `mediaType` / `asin` / `narrator` fields (was silently dropping them).
- **Delete downloaded files from the UI.** Book detail page gains a red "Delete file" action that wipes the on-disk file (ebook) or folder (audiobook) and flips the book back to `wanted`, plus a "Delete book + files" action that removes the record and its files in one go. New endpoints: `DELETE /api/v1/book/{id}/file` and `DELETE /api/v1/book/{id}?deleteFiles=true`. A `bookFileDeleted` history event is recorded so the deletion is auditable.
- **Skip OpenLibrary "works" whose title equals the author's name.** An upstream OL data-quality bug occasionally emits works (e.g. `OL29342228W` for Jared Diamond) where the Work record was never given a title and the API falls back to the author's name. These polluted the Wanted page and produced nonsense destination folders like `Jared M. Diamond/Jared M. Diamond ()`. `FetchAuthorBooks` now filters them out at ingest time and counts the skips in its summary log.

## [v0.4.2] — 2026-04-12

### Light mode

#### Added
- Light theme using a slate palette, with an iOS-style toggle in **Settings → General → Appearance**. First-load default respects the browser's `prefers-color-scheme`; saved preference lives in `localStorage` under `bindery.theme` and syncs instantly across tabs via the `dark` class on `<html>`.
- Pre-paint bootstrap script in `index.html` applies the saved theme before React hydrates, eliminating the dark-to-light flash on page load.
- New `useTheme` hook (`web/src/theme.ts`) and `ThemeToggle` component (`web/src/components/ThemeToggle.tsx`) that both modules outside Settings can reuse later.

#### Changed
- Every hardcoded `zinc-*` utility class across the UI (App shell, all 10 pages, Pagination, AddAuthorModal) now has a paired `dark:` variant. Light mode is the default, dark mode activates when `<html>` has the `dark` class. No semantic-color token refactor — just the standard Tailwind class-based strategy.
- `tailwind.config.js` was already set to `darkMode: 'class'` — no config change needed.

## [v0.4.1] — 2026-04-12

### Security & quality patch

#### Fixed
- Rebuilt against go1.25.9, clearing 17 stdlib CVEs reachable via the API, TLS, and URL-parsing paths (most notably GO-2026-4870 TLS KeyUpdate DoS, GO-2025-4012 cookie memory exhaustion, GO-2025-4009 PEM quadratic complexity, GO-2025-4007 x509 name-constraint quadratic).
- Repaired `.golangci.yml` — removed `gosimple` (absorbed into `staticcheck` in lint v2) and dropped `continue-on-error` on the lint job. The lint CI gate had been silently failing since the v2 upgrade.
- qBittorrent client no longer panics on session-expiry retry when `http.NewRequestWithContext` fails — the error is now propagated instead of calling `Do` on a nil request.
- API handlers that take `{id}` in the URL path now return HTTP 400 for non-numeric IDs instead of silently acting on ID 0. New `parseID` helper in `internal/api/helpers.go` consolidates the pattern.
- Library-scan importer no longer dereferences nil pointers when a book or author lookup fails; lookup errors are logged and the file falls through to the unmatched-import path.
- History-blocklist handler logs corrupt JSON `data` columns instead of silently returning a zero-value event to the client.
- SQL UPDATE in `downloads.UpdateStatus` no longer interpolates a column name via `fmt.Sprintf`. Three explicit statements, one per known status, with the column name as a fixed literal.
- Primary HTTP server now sets `ReadHeaderTimeout` / `ReadTimeout` / `WriteTimeout` / `IdleTimeout` instead of running with the defaults (which are effectively unlimited). Mitigates slow-loris and resource-exhaustion attacks on the public API surface.

#### Added
- Startup warning logged when `BINDERY_API_KEY` is unset, making it obvious that `/api/v1/*` is unauthenticated.
- Helm chart `deployment.yaml` now sets a hardened pod+container `securityContext`: `runAsNonRoot: true`, `runAsUser: 65532`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile.RuntimeDefault`, plus a writable `emptyDir` mounted at `/tmp`.
- CI workflow now auto-creates a GitHub Release with notes extracted from the matching CHANGELOG section on every `v*` tag push. Title is derived from the first `###` subheading. Idempotent — updates notes if a release already exists.

#### Changed
- Dockerfile base image pinned to `golang:1.25.9-alpine` (was `golang:1.25-alpine`) and runtime switched to `gcr.io/distroless/static-debian12:nonroot` with `USER nonroot`.
- `go.mod` bumped to `go 1.25.9`.
- Internal DB queries using `sql.ErrNoRows` comparison now use `errors.Is` for wrap-safety.

#### Frontend
- Fixed four missing `reset` dependencies in `useEffect` hooks on Authors / Books / History / Wanted pages (react-hooks/exhaustive-deps).
- Extracted `usePagination` hook into its own file (`web/src/components/usePagination.ts`) so `Pagination.tsx` is a pure component module (fixes react-refresh/only-export-components).

## [v0.4.0] — 2026-04-12

### Search overhaul

Inspired by the matching patterns in Readarr, Sonarr, and LazyLibrarian.
Fixes the long-standing "short titles get junk results" problem (e.g.
searching "The Sparrow" by Mary Doria Russell no longer returns unrelated
sparrow-themed books, comics, and music releases).

#### Added
- **Four-tier query fallback** in `BookSearch`: `t=book` → `surname+title`
  → `author+title` → title-only. The new surname+title tier disambiguates
  short titles without the noise of full-name queries that some indexers
  fail to match.
- **Word-boundary keyword matching** (`\b...\b`) everywhere in the filter
  and language checks. `sparrow` no longer leaks into `sparrowhawk` or
  `sparrows`.
- **Contiguous-phrase matching** for multi-word titles. A release must
  contain the title words together; scattered occurrences no longer pass.
- **Subtitle handling** for `Title: Subtitle` books. "Dune: Messiah"
  accepts releases tagged as either "Dune" or "Dune Messiah".
- **Composite ranking score**: quality × 100 + edition tag (RETAIL +50,
  UNABRIDGED +30, ABRIDGED −50) + year-match (±20/10/5) +
  log₁₀(grabs) × 10 + size tiebreaker + ISBN exact-match +200.
- **Release parser** (`internal/indexer/release.go`): extracts year,
  format, RETAIL/UNABRIDGED/ABRIDGED flags, release group, and ISBN from
  NZB titles.
- **Blocklist consulted during search** (both manual and auto-grab). The
  infrastructure existed but was never wired into the search flow.
- **Download quality populated on grab** via the new release parser, in
  both the manual grab handler and the scheduler auto-grab path.
- 23 new unit tests covering the matching and ranking pipeline.

#### Fixed
- Scheduler now resolves and passes the book's author name to `SearchBook`
  (previously always empty, which silently disabled the `t=book` tier,
  the `author+title` tier, and the filter's surname anchor for every
  automated search).
- Foreign-language tag filter now word-boundary-anchored. The tag `RUSSE`
  (French for "Russian") was substring-matching inside `RUSSELL`, causing
  books by authors named Russell, Russ, Russo, etc. to be rejected as
  Russian-language releases.

#### Changed
- `Searcher.SearchBook` signature: now takes `MatchCriteria{Title, Author,
  Year, ISBN}` instead of `(title, author)` so ranking can use year and
  ISBN signals.

#### Deliberately out of scope
- qBittorrent grab path and `Download.Protocol` handling (bigger refactor
  planned separately).
- Readarr-style user-facing Quality Profiles (overkill for a single-user
  tool; hardcoded weights serve 95% of cases).

## [v0.3.0] — 2026-04-12

### Added
- Mobile browsing support: responsive layout, hamburger nav, card views
  for History / Blocklist, agenda view for Calendar.
- Blocklist-from-history action for grabbed/failed events (one-click add).
- Preferred language filter for download search results (English default).
- Quick search filter on the Wanted page.
- Inline edit + enable/disable toggles for indexers, clients, and
  notifications in Settings.
- GitHub profile link in the footer.
- "No results" message when indexer search returns empty (previously
  silent).

### Fixed
- Scanner false matches; tightened title matching in library scan.
- Non-English books incorrectly ingested from OpenLibrary author works.
- `imported` books now display as "In Library" in Books page; removed the
  transient `downloaded` filter.
- Version badge only shown for tagged releases; short SHA for branch
  builds.

### Changed
- CI pushes `:latest` image tag on version-tag builds.
- Image SHA tags shortened to 7 chars.

## [v0.2.0] — 2026-04-12

### Added
- Full Readarr feature parity: tag system, metadata profiles, import
  lists, quality profiles with cutoffs, custom formats, delay profiles,
  notifications, backup/restore, and API key authentication.
- Authors / Books / Wanted / History / Blocklist list pagination.
- History page shows error details; grab events are recorded.
- Download error messages surfaced in queue UI.
- `downloaded` status filter + badge on Books page.
- App logo and favicon.

### Fixed
- OpenLibrary author works endpoint now used for accurate book fetching.
- Author search results show top work, book count, and ratings.
- Version / commit / build-date injected into Docker image via ldflags.

## [v0.1.0] — 2026-04-11

Initial public release.

### Added
- Author monitoring with OpenLibrary metadata.
- Per-book status workflow (wanted → downloading → downloaded → imported).
- Series tracking with dedicated page.
- Edition tracking (format, ISBN, publisher, page count).
- Library scan for pre-existing files.
- Newznab / Torznab indexer support with parallel querying.
- SABnzbd download client integration.
- qBittorrent client (scaffolded).
- Automatic import with naming template tokens (`{Author}`, `{Title}`,
  `{Year}`, `{ext}`).
- Cross-filesystem move support (atomic rename → copy+verify+delete).
- Webhook notifications for grab / import / failure events.
- Google Books and Hardcover.app as enricher metadata sources.
- Single-binary distribution with embedded React frontend.
- Distroless Docker image and Helm chart.

[v0.5.2]: https://github.com/vavallee/bindery/releases/tag/v0.5.2
[v0.5.1]: https://github.com/vavallee/bindery/releases/tag/v0.5.1
[v0.5.0]: https://github.com/vavallee/bindery/releases/tag/v0.5.0
[v0.4.2]: https://github.com/vavallee/bindery/releases/tag/v0.4.2
[v0.4.1]: https://github.com/vavallee/bindery/releases/tag/v0.4.1
[v0.4.0]: https://github.com/vavallee/bindery/releases/tag/v0.4.0
[v0.3.0]: https://github.com/vavallee/bindery/releases/tag/v0.3.0
[v0.2.0]: https://github.com/vavallee/bindery/releases/tag/v0.2.0
[v0.1.0]: https://github.com/vavallee/bindery/releases/tag/v0.1.0
