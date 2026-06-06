# Changelog

All notable changes to Bindery are documented here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com) and versions follow
[Semantic Versioning](https://semver.org).

## [Unreleased]

## [v1.17.0] — 2026-06-06

### Added

- **Post-import drop folder for external library tools** (#941) — a new way to hand finished downloads to a tool that *owns* your library (Calibre-Web-Automated, Calibre auto-ingest, Storyteller). With Import Mode set to **External**, Bindery now renames each completed download into a configurable **drop folder** (copy or hardlink — never a move, so torrents keep seeding) instead of leaving it in the download directory, then reconciles the managed copy the external tool produces on the next library scan. Previously this was impossible: normal modes wrote into the library *and* the CWA mirror (so the file landed in both places and CWA fought Bindery over the library dir), while plain External mode left the file unrenamed in the download dir. Configurable layout (`flat` file in the folder root vs `templated` `{Author}/{Title}/…` tree) and placement (`copy`/`hardlink`) under Settings → General → File Naming. New settings `import.drop_folder` / `import.drop_layout` / `import.drop_link_mode`; the existing `cwa.ingest_path` mirror is unchanged for the "Bindery owns the library" topology. Fixes the recurring CWA ingest-routing reports and is the foundation for Storyteller pair-gated handoff (#942). See docs/DEPLOYMENT.md → "Handing off to another library tool".
- **Outbound proxy support** (#986) — route Bindery's remote-facing outbound HTTP through an `http`/`https`/`socks5` proxy via `BINDERY_OUTBOUND_PROXY` (e.g. a VPN container's Privoxy on `:8118`, or `socks5://gluetun:1080`), the same capability Sonarr/Radarr expose. In scope: indexer searches, metadata/cover providers, webhook notifications, and the telemetry ping; download clients and OIDC discovery stay direct. LAN / loopback / single-label destinations (e.g. a Docker `prowlarr` / `jackett`) are dialled direct by default so a local indexer manager stays reachable — tune with `BINDERY_OUTBOUND_PROXY_BYPASS_LOCAL` (default `true`) and `BINDERY_OUTBOUND_PROXY_NO_PROXY` (comma-separated hosts / domain suffixes / CIDRs). Parsed once at startup into a shared, proxy-aware transport in `internal/httpsec`; credentials travel in the URL userinfo and are never logged. Env-var only — no new dependency (stdlib dials all three schemes).
- **Refresh metadata for every author** (#863) — a one-click "Refresh all metadata" background job on the Authors page that re-fetches each author's metadata and catalogue, with progress that survives a page reload. Useful after a first import to backfill descriptions, covers, and missing books.
- **Per-indexer priority is now editable in the UI** (#1009) — the add/edit indexer form exposes the priority the searcher already uses to break ties when the same release is found on multiple indexers (e.g. prefer your Usenet indexers over torrent ones). Manually-added indexers previously had no way to set it, so it was stuck at 0; default is unchanged.

### Changed

- **Loopback URLs are now allowed for admin-configured service endpoints** — download clients, indexers, Prowlarr, the Audiobookshelf base URL, and the Calibre plugin URL now accept `http://127.0.0.1:…` / `localhost` (new `PolicyLANLoopback` SSRF tier). Previously the SSRF guard blocked all loopback with no escape hatch, which made a legitimate, common topology impossible: a companion service bound to `127.0.0.1`, or both containers on `network_mode: host`, could not be reached (e.g. SABnzbd on `127.0.0.1:50155` was rejected with "url not allowed: points to loopback address"). These endpoints are admin-only and CSRF-gated, so the loopback block bought ~no security. **Untrusted paths are unchanged**: proxied cover images and outbound webhooks still block loopback, and link-local + cloud-metadata (e.g. `169.254.169.254`) remain blocked everywhere. The release/torrent download URLs returned by indexers also still block loopback (a malicious indexer must not be able to point Bindery at internal services).

### Fixed

- **Transmission: indexer links that redirect to a magnet now work** (#1006) — public trackers (The Pirate Bay, Knaben, …) surfaced via Prowlarr/Jackett often serve an `http(s)` download link that 30x-redirects to a `magnet:` URI. Bindery fetched that link with Go's HTTP client, which tried to follow the redirect and failed with `unsupported protocol scheme "magnet"`, so the grab never reached Transmission. The Transmission client now follows redirects manually (re-validating each hop against the SSRF policy) and hands a redirected magnet straight to Transmission's `filename` arg — matching how the qBittorrent client already behaves (`internal/downloader/transmission/client.go`).
- **Library scan now records a result on early returns** (#965) — when the library directory was literally unset, or the book listing failed mid-scan, `ScanLibrary` returned without persisting anything, so the Settings → General "Library Scan" section kept showing a stale prior scan instead of reflecting the failure (and the #962 "no files found" warning never fired for an unset directory). Both paths now persist a scan result carrying a `scan_error` message ("library directory not configured" / "scan failed: …") that the UI renders the same way as other scan-outcome warnings. Additive, backward-compatible result field (`scan_error`); the matching logic is unchanged (`internal/importer/scanner.go`).
- **Retired the dead CSV `searchOnAdd` column** (#966) — after CSV import began always fetching each author's catalogue (#963), the optional third `searchOnAdd` column no longer gated anything and never triggered downloads. It is now dropped from the documented format and treated as ignored. The parser stays lenient, so existing users' three-column files keep importing unchanged; the dead field/plumbing is removed (`internal/migrate/csv.go`).
- **Backlist books no longer show a misleading "Wanted" pill** (#977) — `status` and `monitored` are orthogonal (every book starts `status=wanted`; backlist siblings are added unmonitored), but the status pill rendered `status` alone, so an unmonitored backlist book read "Wanted" while correctly never appearing on the Wanted page (which lists `status=wanted AND monitored`). A shared `bookStatusBadge` helper now makes the pill monitored-aware across the book detail page, book lists, and author rows: `wanted` + monitored → "Wanted"; `wanted` + unmonitored → "Not monitored" (muted). No status/model/DB change.
- **Authors and Books pages now reach past the first 100 entries** (#1010) — the list, search, sort, and filters are applied server-side and paginated, so libraries with more than 100 authors or books are fully browsable. Previously only the first page loaded, author/book name search was limited to that page, and the footer always read "1–100 of 100". Author/book name search is now matched on the server (book search also matches the author's name).

## [v1.16.1] — 2026-06-03

A getting-started / onboarding friction pass plus a batch of fixes from user reports. No breaking changes.

### Added

- **Test a download client / indexer before saving** — the Add and Edit forms for download clients and indexers now have an inline **Test** button that probes the unsaved host/port/URL/credentials, so a wrong value can be caught and fixed in place instead of having to save a broken entry, find Test on the saved row, reopen the editor, fix, and re-save. Backed by two new admin-only test-by-config endpoints (`POST /downloadclient/test` and `POST /indexer/test`) that validate and probe a posted config without persisting it; the response shape mirrors the existing test-by-id endpoints so the UI reuses one rendering path.
- **Bulk "Refresh metadata" action on the Authors page** — refresh the catalogues of many selected authors at once (metadata fetch only, never an auto-download). Recovers authors imported with empty catalogues without clicking per-author Refresh one at a time (`internal/api/bulk.go`, `web/src/pages/AuthorsPage.tsx`).
- **First-run onboarding guidance on empty Authors/Books pages** — when a brand-new instance has no authors/books *and* no indexers or download clients configured, the empty state now shows a short "Getting started" block linking to **Settings → Indexers** and **Settings → Download Clients**, explaining they must be configured first (without them, adding an author or searching silently does nothing). The block only appears when both are empty and falls back to the normal empty state if the checks fail. Settings tabs are now deep-linkable via `?tab=indexers` / `?tab=clients`.

### Changed

- **A page crash no longer blanks the whole app** — page render errors are now caught by a route-scoped error boundary that shows the error inline while keeping the nav/header usable, and clears itself when you navigate to another page (no reload needed). Previously the only boundary was the root one, whose full-screen dark-mode fallback took over the entire viewport and required a manual reload to recover.

### Fixed

- **Re-bind metadata "Re-bind anyway" override was unreachable** — when re-binding a book to a provider whose record belongs to a different author, the API returns a `409` carrying `force_required` so the UI can offer an override. The web API client discarded the HTTP status and JSON body on error, so `RebindModal` never saw the flag and rendered the raw `author mismatch` text with no way forward. The client now throws a structured `ApiError` (status + body); the amber "Re-bind anyway" confirmation works as intended. Note: forcing past the mismatch re-points the book's metadata to the new record but keeps it filed under its current author.
- **Audiobook downloads could hang in "downloading" forever (qBittorrent)** — the main import poll (`checkQbittorrentDownloads`) queried only the client's ebook `Category`, so torrents grabbed under `CategoryAudiobook` were never returned and their downloads never matched (logged as `download not found in torrent list`), leaving them stuck. The #700 fix that polls both categories had only been applied to the stall/health adapters, not this poll. It now polls every category the client may have grabbed under via `CategoriesToPoll`. Transmission and Deluge are unaffected (Transmission does not split audiobooks by category/dir; Deluge polls all torrents).
- **Grab errors name the missing download-client protocol** — grabbing a usenet (NZB) release when only a torrent client is enabled (or vice versa) returned a generic "no enabled download client configured", sending users to re-check the client they had already verified. The error now names the release's protocol and, when a client of the *other* protocol is enabled, spells out the mismatch and which client to add (`internal/api/queue.go`).
- **SABnzbd connection test validates the configured category** — the SABnzbd test only checked reachability and discarded the category list, so a typo'd category passed; downloads then landed silently in SAB's default category and the poller never found them. The test now verifies that each configured category (and audiobook category, if set) exists in SABnzbd, returning an actionable error otherwise — mirroring the existing NZBGet behaviour (`internal/downloader/sabnzbd/client.go`, `internal/downloader/adapter.go`).
- **CSV author import now always populates each newly-created author's book catalogue** — instead of only doing so for rows with an explicit `searchOnAdd=true` third column. Plain-name and two-column rows previously created authors with empty catalogues, so the library scan matched no files and the library looked empty after import. The fetch never auto-downloads (`internal/migrate/csv.go`).
- **Library scan surfaces the paths it walked and explains zero/unmatched results** — the manual library scan now records the resolved library and audiobook roots, plus an explicit zero-files signal, in its persisted result. The Settings → General "Library Scan" section shows which paths were scanned, warns by name when no book files were found under the configured directory (a common `BINDERY_LIBRARY_DIR` misconfiguration), and hints when files were found but none matched a book (the author's book catalogue needs populating first). Additive, backward-compatible result fields (`library_dir`, `audiobook_dir`, `scanned_paths`, `no_files_found`); the matching logic is unchanged.
- **Helm chart shipped unusable defaults** — `values.yaml` defaulted `BINDERY_LIBRARY_DIR` to a maintainer-specific path (`/media/BOOKS/incoming`) and enabled a `/downloads:/media` path remap, so a fresh `helm install` pointed the library at a path that didn't exist in the pod and silently rewrote download paths. Library dir now defaults to `/books` (matching the Docker image) and the remap is commented out by default. The chart's ingress was also Traefik-`IngressRoute`-only; it now supports `ingress.type: standard` to render a portable `networking.k8s.io/v1` Ingress (with `className`/`annotations`/`tls`) for nginx-ingress, GKE, EKS, etc. `type` defaults to `traefik`, so existing installs are unchanged.

### Docs

- **Added `docs/QUICKSTART.md`** — an in-repo zero-to-first-download walkthrough (run → first login → indexer → download client → author → grab) with the SSRF `localhost`-rejection gotcha and protocol-matching/category troubleshooting callouts. Linked from the README documentation table.
- **Fixed doc/code drift:** Unraid template Overview now says MIT (was "Apache 2.0"); `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` added to the README env table; `BINDERY_TRUSTED_PROXY` and `BINDERY_TELEMETRY_DISABLED` added to the DEPLOYMENT env-var reference; the broken `#reverse-proxy` anchor in `DEPLOYMENT.md` now points at the Reverse-proxy wiki; README "full reference" wording corrected to point OIDC/forward-auth vars at `docs/auth-oidc.md` / `docs/auth-proxy.md`.

## [v1.16.0] — 2026-06-03

Security and hardening release. The bulk of this version is an audit-driven hardening pass (the **D1–D4** access-control findings and the **Wave 2–5** robustness sweep), opt-in per-user data isolation, a batch of performance work, and a long tail of import/scheduler correctness fixes. No breaking config changes, but two behaviour changes worth noting before upgrading: list endpoints are now paginated and request bodies are capped at 1 MiB by default (see **Changed**).

### Added

- **Opt-in per-user data isolation via `BINDERY_ENFORCE_TENANCY`** (#898, #899) — a new environment variable (defaults **off**) that turns on real per-user scoping. With it set, each user sees only their own authors, books, profiles, and root folders (Tier-1 resources, #899), and the join-scoped queue, history, pending grabs, and OPDS catalogue are scoped to the requesting user (Tier-2, #898). Left unset, Bindery behaves exactly as before — a single shared library view across all accounts — so existing single-user and trusted-multi-user installs are unaffected. Admins still see all data regardless of the flag. Documented in `docs/multi-user.md`.

- **Blocklist entries record who created them** (#929) — a `created_by_user_id` audit column on the blocklist so manual blocks are attributable in multi-user installs. Part of the D4 audit follow-up.

### Changed

- **List endpoints are now paginated** (#902) — the `List` API surface (books, authors, queue, history, etc.) returns paginated results with new sort/lookup indexes backing them, rather than unbounded full-table responses. This keeps large libraries responsive but is a **response-shape change** for any external script that assumed a single call returned every row; such callers must follow pagination. The React UI already paginates.

- **Request bodies are capped at 1 MiB by default** (#901) — every API handler now rejects bodies larger than 1 MiB to bound memory use from hostile or malformed requests. Normal API and UI traffic is well under this; only unusually large custom payloads are affected.

- **Webhook notifications emit an Apprise-compatible `body`/`title` payload** (#888) — the generic webhook now includes top-level `body` and `title` fields alongside the existing structured payload, so Apprise, ntfy, and similar relays render notifications without a custom template.

- **Database backups now use `VACUUM INTO`** (#900) — backups capture a consistent snapshot that includes outstanding WAL pages, rather than copying the main database file and risking a torn or stale backup under load.

- **Sessions are invalidated on password change** (#896) — changing a user's password now revokes that user's existing sessions, so a rotated credential actually logs out the old session everywhere instead of leaving it valid until expiry.

- **Performance sweep (Wave 3)** — pooled HTTP clients for indexers and downloaders so each request no longer spins up a fresh transport (#917); bounded goroutine fan-out on bulk, queue, and series operations to stop large libraries spawning unbounded concurrency (#918); capped the metadata TTL cache and cached `enrichBook` results (#915); debounced Audiobookshelf enumerator checkpoint writes (#916). Build noise reduced by quieting the Vite build warnings (#913).

- **Removed the dormant tag surface** (#927) — the unused tag UI/columns flagged in the audit (D4a) were removed rather than left as dead, unreachable surface.

### Fixed

- **Unmatched audiobook imports no longer panic the library scan** (#946) — a completed download detected as an audiobook but with no resolved book row (no `BookID`, a lookup error, or a row deleted between grab and import) dereferenced a nil book while computing its destination and crashed the scan goroutine. It now fails that download with an actionable status, matching the ebook path. Found in a code sweep.

- **Connection-refused diagnostics stop blaming a Docker subnet** (#944) — the download-client "connection refused" hint now points at the real common causes (a service bound to `127.0.0.1` refusing a LAN-IP URL, or a host firewall) instead of asserting a Docker subnet that host-networked deployments don't have.

- **History tab is no longer empty for auto-grabs** (#938) — scheduler-initiated grabs now record a `Grabbed` history event like manual grabs always did, so monitored-author auto-search produces a visible audit trail. Reported by ThatGuyHere.

- **Calibre import persists series and series membership** (#936, half of #905) — importing from a Calibre library now writes `series` and `series_books` rows, so series populated in Calibre survive the import instead of being dropped.

- **Torrent imports only take the torrent's own file list** (#933, closes #903) — single-file torrents sharing a download root no longer drag unrelated sibling files into the library; the importer operates on the torrent's file list rather than the whole directory.

- **Legacy date formats are tolerated on read** (#921, closes #914) — `Scan` on book/author rows now parses pre-existing non-RFC3339 date strings instead of erroring, and writes back RFC3339 going forward, so databases carried over from older versions load cleanly.

- **Library-scan completion log names every walked root** (#937) — the log line now lists all scan roots instead of only the primary library directory, so multi-root setups can see what was actually covered.

- **Hardcover author-works filter respects canonical-book language** (#890, #889) — the canonical book's language is propagated so the author-works language filter actually applies.

- **`refreshMetadata` guards against `(nil, nil)` from the aggregator** (#892) — a provider returning no book and no error no longer trips a nil dereference.

- **`book.Author` is populated in `List` and `Get` responses** (#884, closes #882) — responses now carry the author object rather than leaving it nil for clients to resolve separately.

- **App-lifetime context plumbed through background goroutines** (#932, #934, closes #846) — bulk, author, calibre, and the four goroutines #932 missed now observe the application lifetime context and shut down cleanly instead of leaking past process exit.

- **`ttlCache` janitor no longer leaks a goroutine per instance** (#881, root cause of #73) — the metadata cache's cleanup janitor is now stopped with its cache, fixing a per-test (and per-instance) goroutine leak.

- **Download/import state-machine and atomicity hygiene (Wave 4)** (#920) — assorted state-transition and atomicity corrections across the downloader and importer.

- **OIDC discovery releases its lock and reads `failedEntry` race-free (Wave 5)** (#919) — the OIDC provider no longer holds a lock across the network discovery call and the failed-entry read is synchronised.

### Security

- **Closed cross-user IDOR on Tier-1 per-user resources** (#899, D1, env-gated) and **scoped Tier-2 join resources** queue/history/pending + OPDS (#898, D3) — see the tenancy entry under **Added**. Both are gated behind `BINDERY_ENFORCE_TENANCY`.
- **Shared deployment-config routes are gated behind `RequireAdmin`** (#897, D2) — indexer/download-client/system configuration endpoints that were reachable by non-admin users are now admin-only.
- **OIDC SSRF, redirect-revalidation, and trusted-proxy scheme guards** (#894) — the OIDC discovery probe and redirect handling reject private/loopback targets (override with `BINDERY_ALLOW_LAN_OIDC`) and re-validate redirect URLs; the trusted-proxy path validates the forwarded scheme.
- **Plugged a settings-endpoint secret leak** (#893) — `auth.oidc.providers` and other settings no longer return stored secrets in API responses (write-only).
- **Library-root containment on file deletion** (#895) — book/author file-deletion endpoints reject paths outside the configured library roots, with symlink resolution, so a crafted path can't delete arbitrary files.
- **StepSecurity best-practices sweep** (#904) — pinned actions, hardened runners, and related CI supply-chain hardening.
- **Go toolchain bumped to 1.25.11** (#945) — picks up the stdlib fixes for `GO-2026-5037` (crypto/x509) and `GO-2026-5039` (net/textproto). CI-toolchain only; the runtime image was already on a patched Go.

### Docs

- **Documentation currency sweep** (#949) — corrected the OPDS path (`/opds/`, not `/opds/v1.2/`), clarified that per-user isolation is opt-in via `BINDERY_ENFORCE_TENANCY`, refreshed the SECURITY.md supported-version table, marked editable quality profiles as shipped, completed the ARCHITECTURE.md package list, and documented previously-undocumented environment variables. The wiki's auth/OIDC recovery and secret-rotation recipes were also corrected.

## [v1.15.3] — 2026-05-29

Patch release. Three user-facing bug fixes plus a Hardcover edition-hydration feature and the client side of the telemetry redesign.

### Fixed

- **Library scan now reconciles Calibre-imported books with missing file paths** (#875, #878) — Calibre import sets every book to `Status=Imported`, but in container setups where Calibre's library mount differs from Bindery's view, `FilePath` stays empty. The library scan's candidate filter required `Status=Wanted` so all imported epubs found zero reconciliation targets, and users had to refresh metadata per author to nudge the rows back into scope. The candidate filter now includes Imported books whose recorded paths are either empty or point at locations that no longer exist, so the canonical Calibre-import-then-scan flow just works. Also covers the related "user moved their library and re-ran a scan" case where Imported rows used to be orphaned forever. Thanks to @Jashun44 for the precise diagnosis.

- **Single-word hyphenated titles no longer have every release dropped at the relevance filter** (#871, #876) — titles whose entire significant content was one hyphenated token (e.g. **Slaughterhouse-Five**, **Mother-to-Mother**) failed the relevance filter against every indexer result because `SigWords` kept the hyphen in the keyword while `NormalizeRelease` on the release side replaced it with a space. `SigWords` now pre-converts the same separator set as `NormalizeRelease` (`._-()[]|`) so hyphenated titles tokenise the same way multi-word titles already do. Reported with root cause and fix shape by @eliseban.

- **Transmission grabs no longer time out when the daemon is behind a VPN container** (#873, #877) — when Transmission runs inside `haugene/transmission-openvpn` or similar, the daemon's outbound traffic routes through the VPN tunnel. Bindery was passing the indexer URL via Transmission's `filename` arg, which makes the daemon fetch the URL itself; through the VPN that fetch never returns and Bindery's 15s deadline trips even though Test-Connection works fine. The Transmission client now fetches the `.torrent` file through Bindery's own HTTP client and submits the content via `metainfo` (base64), matching the same shape SAB (#864) and NZBGet (#837) ship since v1.15.2. Magnet links still pass through unchanged. Same `httpsec.PolicyLAN` SSRF guard and 50 MB cap as the other clients. Reported by @Bclark117 with the exact fix shape this release implements.

### Changed

- **Hardcover editions hydrate into the local library when a book has a confident Hardcover identity** (#822) — when a book carries an `hc:` foreign ID (created via Hardcover lookup, list sync, rebind, recommendations, or series fill), Bindery now fetches the full Hardcover edition list and persists those rows alongside the book. Edition fields use a COALESCE NULLIF upsert so user-curated and import-time values are never overwritten, and edition rows that already belong to another book are silently skipped (no re-parenting). When the matched audiobook edition carries an ASIN and the book has none yet, the ASIN is promoted onto the book and (if Audnex is configured) the audiobook is enriched automatically. Gated on Enhanced Hardcover being enabled (token configured + admin opt-in + env enabled), so installs without a Hardcover token see no behaviour change. Thanks to @magrhino.

- **Telemetry client sends per-subsystem feature counters** (#872) — the daily anonymous ping now carries an optional `features` block with counts of enabled indexers, download clients, notifications, and users, plus booleans for whether Calibre / Audiobookshelf / Grimmory / Hardcover / OIDC / multi-user is configured. Strictly numeric or boolean, never names or values. Lets the maintainer prioritise feature work against actual adoption rather than Discord vibes. Documented at [getbindery.dev/telemetry-fields](https://api.getbindery.dev/telemetry-fields) and opt-out remains unchanged (`BINDERY_TELEMETRY_DISABLED=true` env var or `telemetry.enabled=false` setting).

### Internal

- Closed #870 with a pointer to the existing `BINDERY_NOTIFICATIONS_ALLOW_PRIVATE=1` env var from v1.15.1 #853; same feature, just hadn't been discovered yet.

## [v1.15.2] — 2026-05-28

Patch release. Two download-client fixes for users on v1.15.1 with broken SAB/NZBGet submissions, a UI refactor that locks the clipboard-fallback pattern from v1.15.1 into a reusable hook, plus housekeeping (gosec annotation, chi dep bump).

### Fixed

- **SABnzbd submissions no longer hang in "Waiting" with a resetting countdown** (#864) — Bindery was using SAB's `mode=addurl`, which makes SAB itself fetch the NZB from the indexer URL. In containerised setups where Bindery and SAB sit on different Docker networks (or only Bindery has DNS/route for the indexer), SAB can't reach the URL and parks the job in retry-backoff forever; Bindery's `sent to downloader` log line was misleading because addurl returned `status: true` regardless. Bindery now fetches the NZB itself through its own HTTP client (which holds the indexer credentials and network path) and submits the content via SAB's `mode=addfile` multipart upload — the same shape the NZBGet client has used since #837. The same `httpsec.PolicyLAN` SSRF guard and 50 MB cap apply. Thanks to @ibsfox for the precise repro (manual `.nzb` upload to SAB worked; the URL-handoff did not).

- **NZBGet rejections now name the actual problem** (#861, #862) — when NZBGet's `append` JSON-RPC returns id 0 (rejection), Bindery's error was `NZBGet rejected download (returned id 0)` with nothing more, which gave users no path forward. The most common cause is that the category configured in Bindery's download-client (e.g. `Audiobooks`) isn't defined in NZBGet's own Settings → Categories — NZBGet silently rejects in that case. Bindery now preflights the category list via NZBGet's `config` RPC before submitting; on mismatch the error names both the missing category and what NZBGet actually has configured. The same check runs at Test-Connection time so the misconfig surfaces when saving the client, not on the first grab. If preflight passes but append still returns 0 (disk full, write-permission on intermediate dir, NZBGet paused with quota reached, invalid NZB content), the fallback error now enumerates those causes and points the user at NZBGet's own log. Thanks to @BraynArts for the report.

### Changed

- **Clipboard-fallback handling is now a shared hook** (#860) — extracts `useClipboardCopy` + `ClipboardManualFallback` from the v1.15.1 inline fix in `SearchDebugPanel` (#850) and applies the same pattern to copy buttons for book file paths, API keys, OPDS feed URLs, and OIDC callback URLs. When the modern Clipboard API is unavailable (plain-HTTP LAN install) and the legacy `document.execCommand('copy')` fallback also fails, the UI now renders the text in a focusable read-only textarea so the user can always copy. Thanks to @magrhino.

### Dependencies

- Bump `github.com/go-chi/chi/v5` from 5.2.5 to 5.3.0 (#839).

### Security (housekeeping)

- Extend `// #nosec` directives on the three `os.Remove` call sites in `removeBookPathScoped` (`internal/api/books.go`) to cover gosec G703 (path-traversal taint analysis) in addition to G304. The paths reaching these calls are already constrained by the importer's `sanitizePath` (strips `..`, `/`, `\`, `:`, etc.), so the alert was a false positive in current code, but the suppression is now explicit and references #865 which tracks the defense-in-depth root-folder containment check.

## [v1.15.1] — 2026-05-27

Patch release. Five security/correctness fixes from a post-v1.15.0 review pass plus two user-visible bug fixes that affect every install (OpenLibrary author-search 403, notifications silently inert for everything but manual grabs).

### Fixed

- **Notifications actually fire on real events now** (#849, follow-up to #799) — before this release the notifier was only wired into the manual-grab path. Auto-grab (Wanted searches, series fill, recommendations, bulk monitor), import success, import failure, and download-client health-check failures all looked configured (`Test` worked, settings saved) but never produced webhooks. `EventGrabbed` now fires on every successful auto-grab, `EventBookImported` on every clean import, `EventDownloadFailed` from the importer's failure paths, and `EventHealth` is edge-triggered when a download client enters the error state (suppressed for the `checking → error` transient and for repeated `error → error` polls so a persistently-broken client doesn't spam every refresh cycle). `EventUpgrade` is intentionally deferred — Bindery does not currently have a distinct upgrade-grab code path. Thanks to @wirecutter313 for the original report.

- **`BINDERY_CONTACT` env var lets each install advertise its own User-Agent contact** (#848) — OpenLibrary's `/search/authors.json` endpoint applies per-User-Agent rate-limiting that the shared default contact pointer (the Bindery project URL) was tripping across the entire fleet, leaving users with HTTP 403 on every "Add author" attempt even though name/title book searches still worked. Setting `BINDERY_CONTACT` to a per-instance email or URL — e.g. `BINDERY_CONTACT=you@example.org` — makes each install's User-Agent distinct and lifts the block. Bare email addresses are auto-prefixed with `mailto:`. The default (project URL) is unchanged for installs that don't set the env var; users hitting the 403 should set it. Thanks to @wirecutter313 and a Reddit reporter for the independent confirmations.

- **Notification webhook URL on a private network can be set with a clear escape hatch** (#799 follow-up) — the bare `url not allowed: points to private network` error now appends a hint pointing at the `BINDERY_NOTIFICATIONS_ALLOW_PRIVATE=1` env var, so users running ntfy / Gotify / Home Assistant on the same Docker network know how to unblock the save. Thanks to @joncrangle.

- **Custom HTTP headers are now editable in the notification UI** (#799 follow-up) — the `Headers` field has always existed in the model and the notifier honoured it, but the UI hardcoded `'{}'` so users had no way to provide an `Authorization` header for ntfy / Gotify / webhook routing. Both Add and Edit forms now expose a "Custom headers (JSON)" textarea with client-side validation and an ntfy auth placeholder. Thanks to @wirecutter313.

### Security

- **Indexer / Prowlarr / Download-client list endpoints behind RequireAdmin** (#844) — before this release, any authenticated user (role `user`, not just `admin`) could `GET /api/v1/indexer/{id}` and read the indexer's API key, or `GET /api/v1/downloadclient/{id}` and read the qBittorrent password. The entire `/api/v1/prowlarr/*` subtree (including Create/Update/Delete/Test/Sync) was ungated and a non-admin could delete an admin's Prowlarr instance. All these routes now require admin role. Tested by `TestSensitiveRoutesRequireAdmin`. Found in the post-v1.15.0 security review.

- **Notification routes behind RequireAdmin** (#799 follow-up) — same shape as the indexer/prowlarr leak: `Notification.Headers` carries arbitrary HTTP auth tokens (ntfy auth, Discord routing), but `GET /api/v1/notification` was ungated. Now admin-only across the whole subtree.

- **Backup endpoints behind RequireAdmin** (#845) — the `POST /api/v1/backup/{filename}/restore` endpoint overwrites the live SQLite database with the named backup file. Before this release, any authenticated user could roll the instance back. Now admin-only.

- **OIDC promote-first-admin race condition fixed** (#845) — two concurrent first-time OIDC logins against an admin-less instance with local auth disabled could both pass the "no admins exist" check and both be auto-promoted to admin. The decision is now atomic via `SettingsRepo.SetIfAbsent` (SQLite `INSERT … ON CONFLICT DO NOTHING`): exactly one concurrent first-time login wins; any other simultaneous login falls back to the default role.

- **Calibre import rollback is now transactional** (#643 follow-up, #847) — when a rollback hit a per-entity failure partway through, prior deletes/restores were already committed but the run wasn't marked rolled back, so retrying re-applied successful actions against shifted state and `restore_*` ops could mis-revert. The entire rollback now runs inside a single `sql.Tx` — any failure rolls back every prior write atomically, and `Stats.Failed > 0` is impossible on a successful return.

- **Migration runner refuses duplicate version numbers at boot** (#845) — the 043 collision incident during the v1.15.0 cycle was silently lost on every existing install (the apply loop skipped the second `043_*.sql` because the version was already recorded). A new startup guard fails with a clear error when two migration files share a numeric prefix, so the failure mode can't recur.

- **ABS / Grimmory / Calibre plugin base URLs validated against SSRF policy at save** (#845) — the admin-input boundary for these provider URLs now blocks link-local (169.254/16, AWS IMDSv4) and cloud-metadata endpoints via `httpsec.ValidateOutboundURL(PolicyLAN)`, matching the existing indexer/prowlarr/downloadclient policy. Loopback and RFC1918 are still allowed for typical homelab deployments. NewClient callers continue to use the format-only `NormalizeBaseURL` so test fixtures with `httptest` (loopback) still construct clients.

- **File handler path check fails closed when no library roots are configured** (#845) — previously allowed any path when `BINDERY_LIBRARY_DIR` was unset (intended for test fixtures, but a silent prod misconfiguration). Now returns 403 unless the path falls under a configured root; tests seed an explicit `t.TempDir()` root.

- **Trusted-proxy `0.0.0.0/0` boot warning** (#845) — operators sometimes set `BINDERY_TRUSTED_PROXY=0.0.0.0/0` to silence the proxy-mode safety gate, but in that shape every client's `X-Forwarded-For` is honoured, defeating the login rate-limiter and any per-IP decision. A boot-time `slog.Warn` makes the misconfiguration visible in logs.

## [v1.15.0] — 2026-05-26

Six feature drops plus a sweep of fixes for default-install breaks that v1.14.2 didn't catch.

### Added

- **Editable Quality Profiles** (#768) — quality profiles existed in the model and had a read-only Settings tab, but no way to create, edit, or delete them existed. The `/api/v1/qualityprofile` endpoint gains CRUD behind `RequireAdmin`, and Settings → Quality Profiles is now a full editor: reorderable preference list, per-format allow toggle, cutoff `<select>` restricted to allowed items, audiobook badge on m4b/mp3/flac/m4a entries. Delete-in-use returns 409 with the conflicting author count. Closes the obvious migration story from *arr that the read-only UI implied but didn't deliver.

- **Configurable author monitor modes** (#792, #809, #810) — adding a prolific author no longer floods Wanted with the full back catalogue. The Edit Author dialog gains a `MonitorMode` selector with five values: `all` (existing behaviour), `future` (only books with a future release date), `latest N`, `none`, and `series` — the last lets the user pick one or more of that author's series and monitor only books in those series. `BINDERY_AUTHOR_DEFAULT_MONITOR_MODE` sets the global default for new authors (any mode except `series`, which is per-author by design). Existing books can be retroactively re-monitored to match the new mode via an "apply to existing" checkbox. Thanks to @anthonysnyder for the original ask and @magrhino for implementing #809.

- **Bulk monitor/exclude/delete on Author detail** (#791) — per-row checkboxes + select-all header with indeterminate state + sticky bulk-action bar. Monitor / Unmonitor immediate, Exclude / Delete behind a confirm. Selection auto-prunes when filters hide rows. Pairs with the new monitor modes for a coherent "add prolific author" workflow. Thanks to @anthonysnyder.

- **Per-media-type download client category** (#700) — a download client now accepts a second `CategoryAudiobook` field. When set, audiobook grabs go under that category and validate against `BINDERY_AUDIOBOOK_DOWNLOAD_DIR`; ebook grabs use the existing `Category` against `BINDERY_DOWNLOAD_DIR`. The fuzzy `strings.Contains(category, "audio")` heuristic is gone. Multi-client setups can now use `PickClientForMediaType` to route by explicit field. Thanks to @strenkml.

- **Calibre import/sync rollback** (#643) — Calibre import gains run tracking + entity snapshots + rollback preview + rollback execute, modelled on the existing ABS rollback. A bad Calibre import is now revertible without a database-level snapshot. Metadata-only revert — on-disk files are not touched. New endpoints under `/api/v1/calibre/runs[/{id}/rollback[/preview]]` behind `RequireAdmin`; Settings → Calibre gains a "Recent imports" panel with per-run Rollback. Sync-side (push to Calibre) intentionally excluded — it only mutates `calibre_id`, no data damage. Thanks to @magrhino.

- **Edit existing Prowlarr connection** (#820) — the Prowlarr card in Settings → Indexers gained Add/Test/Sync/Delete but never Edit, so rotating the Prowlarr API key required deleting and recreating the instance (which cascade-deleted every synced indexer row and the user's local toggles/priorities). New Edit form is wired to the existing `PUT /api/v1/prowlarr/{id}`. Key rotation propagates immediately to every indexer row managed by the instance, so synced indexers never authenticate with a stale key. URL changes still need a manual Sync to rebuild per-indexer torznab URLs; the form warns when URL is edited. Thanks to @magrhino.

- **Hardcover series refs populated during list sync** (#805) — Hardcover GraphQL queries for custom lists and built-in shelves now request `featured_series` fields; the list syncer wires those refs into `SeriesRepo.LinkSeriesRefs` best-effort after each book create. Series links appear immediately for books imported from Hardcover lists rather than requiring a later manual rebind. Thanks to @magrhino.

### Fixed

- **OpenLibrary name/title searches no longer fail with HTTP 403** (#834) — OpenLibrary's API now blocks requests whose `User-Agent` does not include a contact pointer (email or URL), and Bindery's previous `bindery/<version> (<os>)` UA matched that block. Name/title book additions failed immediately against OpenLibrary while ISBN lookups (which fall through to Hardcover enrichment) still worked, so the breakage was easy to miss in smoke checks. The User-Agent now appends the project URL — `bindery/<version> (<os>; https://github.com/vavallee/bindery)` — which satisfies OpenLibrary's policy and unlocks its higher rate limit. Thanks to @thetic for the precise repro and root-cause analysis.

- **qBittorrent on Windows reports paths with backslashes; Bindery now normalizes them** (#800 follow-up) — a qBittorrent instance running on Windows reports `SavePath`, `ContentPath`, and category `SavePath` with backslash separators (e.g. `N:\Torrents\complete\library\book`). Bindery's downstream Linux path code (`filepath.Walk`, `PathRemap.Apply`, `pathIsAtOrUnder`) cannot process those, so a Docker-on-Windows user's import failed with `no book files found in N:\...` even though their PathRemap looked right. Backslashes are now normalized to forward slashes at the qBit API boundary; PathRemap configuration becomes predictable across deployments. Reported by PixieApples on Discord.

- **qBittorrent path remap is now verified against the on-disk filesystem** (#800 follow-up) — the PathRemap suggestion shipped in v1.14.2 only checked whether the remapped string was at-or-under `BINDERY_DOWNLOAD_DIR`, never that the resolved path actually existed. Linux is case-sensitive, so a Windows/WSL/Docker user with their drive mounted at `/N/` but Bindery configured with `/n/` (or vice versa) got a textually-correct remap suggestion that silently pointed at nothing. The health check now stat()s the resolved path; if missing but a case-variant exists, the error names the divergent segment so the user knows exactly which letter to fix. Also reported by PixieApples.

- **qBittorrent import diagnostics now surface real failure modes** (#824) — three gaps made torrent import failures near-invisible even with debug logging on: `GetTorrents` API errors were debug-level, loop-level skip-on-no-match was silent, and the "no book files found" error didn't distinguish path-doesn't-exist from path-exists-but-empty. API errors now log at Warn, the poll loop emits per-skip and per-match debug context, and when the download path doesn't exist a separate Warn names PathRemap as the likely fix. Thanks to @statte.

- **Docker → bare-metal host firewall is now named in the ECONNREFUSED hint** — `(service may not be running on that port)` was misleading when the service was running and a host firewall was REJECTing traffic from the Docker bridge subnet (REJECT sends RST → ECONNREFUSED). The hint now reads `(connection refused — service may not be listening on that port, or a host firewall is rejecting traffic from the Docker subnet)`. Reported by Daize on Discord.

- **Grimmory integration is now honest about being configuration-only** (#818) — Grimmory v3.x has no API keys (confirmed upstream at grimmory-tools/grimmory#1487), but the Bindery UI implied the API key was required, sending users on wild-goose chases when they tried to find one. The field is now optional with a clear note, the empty `Authorization` header is suppressed when no key is set, and a banner at the top of the tab acknowledges that Bindery does not yet push books to Grimmory — that's tracked in #826. Reported by Merijeek.

- **Migration version collision (043)** (#832) — internal hotfix during the v1.15.0 cycle: two unrelated PRs both shipped a `043_*.sql` migration and the runner skips any version it has already applied, so on existing installs only the first 043 would run and the second's schema change would be silently lost. The author-monitor-mode migration was renumbered to 045 before any release shipped the collision.

### Changed

- **Per-media-type expected-dir resolution** (#700 ripple) — `ExpectedDownloadDirForClient` now takes a `mediaType` parameter rather than guessing from category name. The fuzzy `strings.Contains(category, "audio")` heuristic is gone.

### Chores

- Dependency bumps via dependabot (#815, #817) and vulnerable Go modules update (#819).
- IndexersTab i18n migration (#838) — Settings → Indexers strings now route through `t()` so they translate per locale.
- Sweep of 113 `t.Fatal` nil-check sites in test files to satisfy staticcheck's SA5011 analyzer (#841); no runtime behaviour change.

## [v1.14.2] — 2026-05-24

### Fixed

- **Hardcover import lists paginate and reuse the saved global token** (#789) — large custom lists previously stopped at the first page, built-in shelves (To Read / Currently Reading / Read / Did Not Finish) did not page through all books, and a global-token deployment had to duplicate the Hardcover token per list to make sync work at all. Built-in shelves now expose item counts in the list picker, both custom lists and shelves paginate fully, and an import list with no per-list override now falls back to the workspace-wide saved Hardcover token. Thanks to @magrhino.

- **qBittorrent re-grab of an already-imported book now resolves to imported instead of looping or being blocked** (#769) — the v1.14.0 hash-recovery fix landed the download row in `StateGrabbed` when qBittorrent returned 409 for a duplicate add, but three gaps prevented it from completing on its own: a missing `content_path` in move-mode left the download polling forever; an empty download directory tripped `StateImportFailed → StateImportBlocked` after three retries; and the `grabbed → completed` transition was rejected by the state-machine guard. Bindery now detects "already in library" via `book_files` + `os.Stat`, walks the import states through to `StateImported`, and the missing transition has been added. Thanks to @statte.

- **"Local-only" auth bypass now grants the admin role to trusted-LAN requests** (#799) — admin-only endpoints (auth mode change, user CRUD, settings writes) returned `admin role required` 403 for requests on a trusted private network, even though local-only mode is meant to grant frictionless access from one. The trusted-LAN bypass now sets the request's role to `admin`, mirroring the API-key bypass.

- **Webhook notification saves surface their errors instead of failing silently** (#799) — saving a webhook notification under Settings → Notifications dropped errors on the floor, so a URL rejected by the default-strict outbound policy gave the user no feedback at all. Add and edit forms now display the error inline and disable the Save button while the request is in flight.

- **qBittorrent category-path mismatch error now tells you how to fix it** (#800) — when a qBittorrent category's save path resolves outside Bindery's expected download directory, the health-check error named the problem but never the remedy. The error now suggests a derived `PathRemap` value (e.g. `/torrents/complete:/downloads`) and the alternative of mounting the same directory inside Bindery to match `BINDERY_DOWNLOAD_DIR`.

- **Adding a new author no longer risks deleting the author row before the requested book is saved** (#804) — for prolific authors the async catalogue sync that follows `AddBook` can exceed the 15s poll budget; when it did, the orphan-cleanup defer treated the just-created author as orphaned and deleted it, and the async goroutine's later inserts cascaded into FOREIGN-KEY failures. The handler now directly persists the requested book before the cleanup defer runs whenever the author was newly created, mirroring the existing DNB path from #667. The async sync continues to do catalogue backfill — it just no longer races for the requested book.

- **Stale ABS no-match review items are auto-reconciled on rescan, plus per-run bulk dismiss** (#767) — fixing metadata in Audiobookshelf and re-running the import previously left old no-match rows in the review queue forever, indistinguishable from genuine no-matches. Bindery now flips pending review rows for any item that matched on the current run (preserving any user-applied decisions on rows that have already left pending), and a new "Dismiss all from this run" button on the ABS review page lets you clear the residue from a previous run.

## [v1.14.1] — 2026-05-22

### Fixed

- **Prowlarr no longer syncs zero indexers** (#763, #788, #794) — since v1.13.x the Prowlarr sync read each indexer's categories from a top-level field that Prowlarr's API does not populate (the category list lives under the indexer's `capabilities`). Every indexer was rejected as having "no book/audiobook categories", and — worse — previously synced indexers were then deleted on each sync, wiping indexer config. The sync now reads categories from `capabilities`, scoped against a registered book application (Readarr/LazyLibrarian) where one exists and falling back to the indexer's own book/audiobook capability categories for standalone Prowlarr setups. The syncer also no longer removes indexers when a sync matches nothing at all, so a future filter regression cannot wipe indexer config again.

- **qBittorrent 5.1.4 category health checks no longer fail** (#793) — qBittorrent 5.1.4 can return a boolean `download_path` flag in category payloads, which made Bindery fail to decode the categories response (`cannot unmarshal bool into Go struct field`) and fail the category health check. Non-string category path fields are now ignored while the string save path is still read.

- **Readarr migration imports author names correctly** (#784) — the Readarr import queried a non-existent `Authors.Name` column; in Readarr's schema the author name lives in `AuthorMetadata`, joined via `Authors.AuthorMetadataId`. The import now performs that join, so migrated authors keep their names.

- **Telemetry server stages its database backup next to the database** (#777) — the telemetry server wrote its backup to `/tmp`, which could fail or cross a filesystem boundary; the backup is now staged alongside the live database.

## [v1.14.0] — 2026-05-21

### Added

- **Audiobookshelf import can reconcile multiple libraries from one source** (#670) — a single ABS source can now import from several selected book libraries (for example a separate Books and Audiobooks library) instead of just one. Libraries are imported in an ordered, per-library sequence, each producing its own run record with independent checkpoint, rollback, and provenance, and the settings UI gains a multi-select for choosing them. Existing single-library configurations are migrated and behave unchanged. Closes #580.

- **Per-author audiobook root folder** (#579) — an author can be assigned a dedicated audiobook destination, separate from the global audiobook directory and from the ebook root folder. Set it in the Edit Author dialog; when unset, audiobooks fall back to the global audiobook directory. It is honoured by both the import scanner and the Audiobookshelf import path. Thanks to @j-tt for the original report and patch.

- **One-shot Goodreads library import** (#585) — a migration aid for users coming from Goodreads or Readarr. Export your library CSV from Goodreads, upload it under Settings → Import, and Bindery resolves each row against the metadata providers, shows a dry-run preview (added / skipped / failed to resolve), and lets you commit. Rows that fail to resolve are downloadable as a CSV to fix and retry. A shelf filter (default: `to-read`) selects which books to bring across. It is a one-time import, not a live sync — Bindery never contacts Goodreads.

- **OIDC role mapping** (#688) — three new opt-in, backward-compatible env vars remove the manual-promotion friction (and lockout trap) of SSO-only deployments. `BINDERY_OIDC_DEFAULT_ROLE` (`user`/`admin`, default `user`) sets the role assigned at OIDC auto-provision time. `BINDERY_OIDC_ADMIN_GROUP` makes the IdP authoritative for the admin role: when set, every login promotes the user to `admin` if the configured group claim contains that group and demotes to `user` if absent — overriding the manual role-promotion API for OIDC users. `BINDERY_OIDC_GROUP_CLAIM` (default `groups`) selects the claim path and tolerates both shapes IdPs emit (a JSON array of strings, or a single space/comma-delimited string). Finally, when local auth is disabled and Bindery has zero admins at OIDC-provision time, the first provisioned OIDC user is auto-promoted to admin — guarded by a one-shot settings flag so deleting all admins later cannot silently re-promote.

### Changed

- **The Wanted page has been redesigned** (#760) — the wanted-books list now uses the same card styling and consistent action buttons as the refreshed book-detail page, presenting each book and its search action more clearly.

- **Audiobookshelf import now imports unmatched items instead of queuing them for review** (#781) — previously every author or book without a confident local match was sent to the manual review queue, so a first import of a large folder-backed ABS library left almost everything (often 90%+) parked for review. Unmatched authors and books are now created directly — a faithful import of your own library — and only genuinely *ambiguous* matches (a close but uncertain candidate) are queued for review. If you re-run an Audiobookshelf import, expect far more items to import directly than before.

### Fixed

- **Readarr import no longer fails CSRF validation** (#765) — the Readarr/CSV migrate upload bypassed the standard API request wrapper and sent no CSRF token, so it was rejected once CSRF enforcement was tightened. The upload now goes through a helper that attaches the CSRF and `X-Requested-With` headers; this also fixes the upload on sub-path deployments.

- **Hardcover series and shelf handling hardened** (#691, #776) — built-in reading-shelf mapping (Want to Read / Currently Reading / Read / Did Not Finish) is corrected against Hardcover's documented `status_id` values, series mapping is more robust, and ISBN, genre, and media-type are now parsed from Hardcover responses.

- **qBittorrent re-grabs no longer fail when the torrent is already present** (#769) — re-grabbing a book whose torrent qBittorrent already holds returned `add torrent HTTP 409: Conflict` and failed the grab outright. A 409 means the content is already available, so Bindery now recovers the existing torrent's hash and proceeds to import instead of erroring.

### Docs

- **Documentation deep-dive cleanup** (#782) — removed a stale orphaned multi-user doc, corrected the multi-user guide (a wrong API route, a non-existent config key, and a wrong env var), updated the Audiobookshelf docs for the new import behaviour, added how-tos for the Goodreads import and the per-author audiobook root folder, and completed the README documentation index.

## [v1.13.2] — 2026-05-20

### Changed

- **The library scan is now linear in library size instead of quadratic** (#757) — reconciling on-disk files against the database ran a Jaro-Winkler title comparison for every *(file, book)* pair, an O(files × books) cost that made the first scan of a large migrated library take minutes and allocate gigabytes of memory. The title comparison is now scoped to the file's author and the loop-invariant title normalisation is hoisted out of the inner loop, so a realistic per-author library scans in time proportional to its size — a 2,000-book library drops from ~3.6 s to ~0.08 s, allocating 98% less memory.

### Fixed

- **The library scan no longer transposes author and title for Readarr- and Calibre-organised libraries** (#754) — the scan inferred author and title by splitting the filename on `" - "`, assuming a `Title - Author` order, but Readarr's default naming and Calibre both write `Author - Title`, so every migrated book scanned in with the two swapped and downstream metadata matching failed. The scan now derives author and title from the `{Author}/{Book}/` folder hierarchy — unambiguous and dash-safe — and falls back to filename parsing only when no such hierarchy is present.

### Docs

- **Added wiki pages for troubleshooting, storage and hardlinking, and migrating from Readarr** (#755) — new guides under `docs/` covering common setup problems, how Bindery's storage layout and hardlinking behave, and the path for bringing an existing Readarr library across.

## [v1.13.1] — 2026-05-20

### Changed

- **The book detail page has been redesigned** (#751) — the scattered action links are now consistent buttons, the file path and per-format on-disk status are surfaced clearly, history entries are humanised, and deleting a book and its files moved to a dedicated "Danger zone" that requires an explicit checkbox confirmation. The media-type control sits with the indexer-search action it scopes.

## [v1.13.0] — 2026-05-20

### Added

- **Session-secret rotation** (#746) — an admin can rotate the session-signing secret from Settings → Security. Rotation keeps the previous secret valid for a one-rotation window so existing logins are not dropped immediately; rotating twice fully invalidates sessions signed with the old secret.

### Security

- **OIDC account-linking now requires a verified email and enforces `AllowedGroups`** (#736) — email-based linking of an OIDC identity to an existing account is rejected unless the IdP marks the email verified, closing an account-takeover vector; a provider's configured `AllowedGroups` is now actually enforced (login is refused when the user belongs to none of them).
- **CSRF protection can no longer be switched off with a bogus `?apikey=`** (#734) — the CSRF and `X-Requested-With` exemptions now key off a *verified* API-key authentication rather than the mere presence of an `apikey` parameter; the API key is also no longer accepted from the URL query string for state-changing requests.
- **`local-only` auth mode is no longer spoofable via `X-Forwarded-For`** (#737) — the client IP is resolved by walking the forwarded-for chain right-to-left and peeling trusted-proxy hops, so a client-supplied leftmost address can no longer masquerade as local; session cookies also carry a key-id to support secret rotation.
- **Indexer requests re-validate the resolved IP on every connection** (#738) — prevents a DNS-rebinding attack where an indexer hostname is repointed at an internal or cloud-metadata address after the initial create-time check.

### Fixed

- **Move-mode imports no longer destroy un-imported or still-seeding files** (#740) — a partial multi-file import no longer marks the download complete and deletes the source of a file that never landed; the import mode is resolved once per run; a cancelled directory copy no longer continues in the background and deletes its source; and move cleanup removes only the specific imported files instead of `RemoveAll`-ing a path that can be a shared torrent save root.
- **Imports interrupted mid-move are recovered instead of wedged forever** (#741) — downloads stuck in `importing` after a crash are swept back to a retryable state on startup; retries are idempotent and no longer double-add files; external-handoff imports use a dedicated state so they no longer cause a silent re-download loop; and a download whose source has vanished ends in a terminal blocked state instead of retrying invisibly forever.
- **Database migrations now run in a transaction** (#733) — a crash mid-migration no longer leaves partially-applied DDL; migration versions are keyed to the filename number (with a one-time reconciliation of older databases) so the numbering gap can no longer cause a migration to be skipped.
- **Scheduled jobs honour the shutdown signal and no longer leak goroutines** (#739) — cron jobs run under the process-lifecycle context so a graceful shutdown can cancel them; background goroutines are tracked and drained on stop; per-format pending releases are no longer dropped when a dual-format book's other format is grabbed first.
- **Indexer errors are classified and no longer wasted on the tier ladder** (#735) — a hard indexer rejection (auth failure, rate limit) on an early search tier now aborts immediately instead of retrying the same indexer through three more tiers; searches also have an overall timeout so one hung indexer cannot stall the whole query.
- **Background recommendation work stops on shutdown** (#732) — the recommendations goroutine is tied to the app lifecycle instead of an uncancellable context.
- **Deleting one format of a dual-format book no longer removes the other** (#742) — a format-scoped file delete only removes files of that format, not the same-named sibling of the other format.
- **Audiobookshelf settings are saved atomically** (#742) — the ABS config is written in a single transaction, so a mid-save failure no longer leaves a half-applied configuration.
- **Book status updates are validated** (#742) — the API rejects an unknown `status` value instead of writing it verbatim.
- **qBittorrent categories that save to a sub-folder of the download directory are accepted** (#744) — the category-path health check no longer requires the category's save path to equal the configured download directory; a path at or under it is valid. A category that saves entirely outside the download directory still warns.

### Changed

- **Removing a queue item keeps the downloaded files by default** (#742) — the queue "remove" action no longer deletes data and stops seeding unless you explicitly opt in via a checkbox in the confirmation dialog.
- **Non-admin users no longer see admin-only Settings sections** (#745) — the Settings page hides sections a non-admin account cannot use (download clients, indexers, auth, system, …); a non-admin still sees Appearance and can still change their own password. Backend authorization is unchanged.

## [v1.12.3] — 2026-05-20

### Security

- **API-key regeneration and OIDC provider management now require an admin account** (#717) — `POST /auth/apikey/regenerate` and `PUT /auth/oidc/providers` sat outside the admin-only route group, so any signed-in non-admin user could rewrite OIDC config or regenerate the API key and read it back — and the API key grants admin access. Both routes are now behind the admin check.
- **NZBGet downloads validate the NZB URL before fetching; SABnzbd API key redacted from errors** (#724) — the NZBGet NZB fetch now runs the same outbound-request (SSRF) policy check qBittorrent already applied, and the SABnzbd API key is no longer interpolated into error messages.
- **Session cookies and CSRF tokens fail closed on a missing or too-short signing secret** (#726) — a missing or under-32-byte session secret previously still produced "valid" HMAC tokens; signing and verification now reject it instead.

### Fixed

- **Saving OIDC provider settings no longer breaks login** (#716) — the provider reload ran on the already-cancelled request context, so discovery aborted and every provider was marked failed until a later retry. It now runs on a non-cancelled context.
- **Scheduled jobs no longer overlap themselves** (#718) — a slow run (e.g. `check-downloads` on slow storage) is now skipped rather than run concurrently with the next tick, which previously could double-import a download. Two swallowed scheduler errors are now surfaced.
- **The hardlink import mode is reachable again** (#719) — the same-filesystem check stat'd the not-yet-created destination path and always failed, so first imports silently fell back to copying (doubling disk use) even when the download and library shared a filesystem.
- **Import retries record a blocked status correctly** (#720) — the `import-failed → blocked` and `→ importing` state transitions were rejected, so a retry that hit a blocking condition burned the retry counter with no state change or recorded reason.
- **The search debug panel no longer shows fabricated relevance rejections** (#721) — the debug relevance path skipped the query-title normalization the live search applies, so titles with edition qualifiers like "(German Edition)" were reported as dropped when the real search kept them.
- **Removing or demoting an admin is now atomic** (#722) — two simultaneous requests could each pass the "is there another admin?" check and both proceed, leaving the instance with zero admins.
- **Approve, Fill series, and blocklist actions are now idempotent** (#723) — re-approving an already-imported Audiobookshelf review item no longer re-imports the book, "Fill series" no longer re-grabs books already downloading or downloaded, and blocklisting the same release twice no longer creates duplicate rows.
- **Download-client tracking and concurrency fixes** (#725) — a SABnzbd job accepted without a trackable NZO id now surfaces as an error instead of becoming silently untrackable; concurrent torrent adds to one Deluge client no longer cross-assign info-hashes; and a qBittorrent session-refresh retry now checks the response status instead of decoding an error page as data.

### Changed

- **Torrents reported as having missing files now show as errored in the queue** (#725) — the queue's error-state check now recognises qBittorrent's `missingFiles` status.

## [v1.12.2] — 2026-05-20

### Fixed

- **Searches for titles containing a common word no longer return zero results** (#699) — The tier-1 canned-feed detector accepted a Jackett/AudioBookBay category feed whenever *any* significant query word coincidentally appeared in *any* result. For a title like *Life Ascending*, the common word "life" matched an unrelated canned title, so the canned feed was accepted as a tier-1 hit and the relevance filter then rejected all of it — leaving the user with zero results instead of falling through to text-search tiers. The detector now requires every significant query word to appear in at least one result before accepting a tier-1 response.
- **Newznab/Torznab indexer errors are surfaced with their own code and description** (#698) — When an indexer returns a top-level `<error code="N" description="...">` element (bad API key, rate limit, site disabled, …) instead of an `<rss>` feed, Bindery reported the raw XML decoder error `expected element type <rss> but have <error>`. The Search debug panel now shows the indexer's actual error, e.g. `indexer error 500: Request limit reached`.

## [v1.12.1] — 2026-05-19

### Added

- **Hardcover edition metadata lookup** (#686) — Hardcover-backed books can now return edition-level metadata, including ISBNs, ASINs, publisher, format, page count, cover, language, and audiobook duration, using Hardcover's current editions query shape.

### Fixed

- **Torznab indexers returning canned category feeds no longer block searches** (#665, #687) — Tier-1 `t=book` results that ignore the title/author params (Jackett/AudioBookBay pattern) are detected by a keyword-relevance check; the search now falls through to text-search tiers instead of returning the same canned results for every query.
- **qBittorrent 5.x grabs no longer surface as failed when the add succeeded** (#690) — qBittorrent v5 returns a JSON body from `POST /api/v2/torrents/add` (`{"success_count":1,"added_torrent_ids":[...]}`) instead of the plaintext `Ok.` v4 returned; Bindery was treating the JSON as a failure. The client now accepts either shape and uses `added_torrent_ids[0]` as the infohash directly when available.
- **Prowlarr sync no longer imports unrelated, disabled, or non-search indexers** (#675) — Indexers disabled in Prowlarr, without `supportsSearch`, or with no ebook/audiobook categories are now skipped during sync. Previously every indexer Prowlarr returned was created in Bindery and would respawn if deleted.

## [v1.12.0] — 2026-05-18

### Added

- **Calibre metadata handoff** (#668) — Calibre pushes now carry Bindery book, author, edition, series, identifier, language, rating, description, and cover metadata through both `calibredb` and metadata-capable Bindery Bridge plugin syncs, with legacy plugin fallback preserved.

### Fixed

- **qBittorrent imports recover from mismatched container paths without re-downloading** (#641) — Download clients now support per-client path remaps, qBittorrent grabs are sent with the expected category save path, and Settings surfaces qBittorrent category path health warnings. Queue items stuck in `importFailed` can also be retried after fixing storage/path settings.
- **Calibre ID reuse** (#668) — Plugin sync no longer reuses a stored source-library Calibre ID when pushing into a different Calibre library.
- **Hardcover supplemental author sync restored** (#669) — Hardcover removed fields from its GraphQL `books` shape and blocked `_ilike` searches, causing author page enrichment to fail with validation or 403 errors. Bindery now uses Hardcover's current search operation and avoids the removed author-work fields.

## [v1.11.2] — 2026-05-17

### Fixed

- **DNB add-by-ISBN no longer fails with "book not found after author sync"** (#667) — The DNB flow used to create the author row, kick off a 15-second background reconcile, and poll for the book to appear from a second SRU query that DNB's index can't actually answer (the synthetic `dnb:gnd:*` / `dnb:author:*` IDs aren't queryable in DNB's bibliographic `num=` or `per=` indices). The poll always timed out, the user saw "try again shortly" forever, and the author row was orphaned on the way out. The AddBook handler now resolves the DNB record synchronously, inserts the book in the same flow as the author, and only falls back to the reconcile poll for non-DNB providers.
- **German titles no longer render with garbage characters around leading articles** (#667) — DNB's MARC 21 records wrap the non-sorting prefix (e.g. "Der", "Die", "Das") in U+0098 (MARC Non-Sorting Begin) and U+009C (MARC Non-Sorting End) C1 control characters. These were passing through `marcClean` untouched and showed up as box/replacement glyphs in titles like *Der war's* by Juli Zeh. The cleaner now strips both control characters at the front of the pipeline, matching the pattern in calibre-dnb's `remove_sorting_characters` helper.
- **Failed adds no longer leave orphan author rows** (#667) — When AddBook timed out polling for a book to land, the author row created earlier in the flow stuck around with zero books. The handler now tracks whether the book was actually written and rolls the author back if not, eliminating the "add author first, then add book" workaround.
- **DNB MARC 100/700 author selection now follows the `aut` relator** — Author resolution falls back through MARC 100 → 700 with `$4 aut` (or German `$e Verfasser` / `Verfasser*in`) → first 700 with any name. Translations and audiobooks where the original author sits only in 700 (e.g. Harry Potter audiobooks where J.K. Rowling is cataloged as `ctb`) now resolve to a usable author instead of dropping the record.
- **Synthetic DNB author IDs short-circuit `GetAuthorWorks`** — When the foreign ID is `dnb:gnd:*` or `dnb:author:*` (a Bindery-internal synthetic, not addressable in DNB's SRU index), the works lookup now returns empty immediately instead of issuing a 15-second nonsense query against the `num=` and `per=` indices.

### Added

- **MVB cover image fallback for DNB-sourced books** — German books that aren't in OpenLibrary or Hardcover were ending up with no cover image. Bindery now consults DNB's public MVB cover service (`portal.dnb.de/opac/mvb/cover?isbn=<X>`) as a fallback when no other provider returned an image URL. Cheap HEAD probe to verify the service is actually serving an image for that ISBN before persisting the URL. Pattern lifted from calibre-dnb (#667).

### Internal

- **Live DNB SRU regression test** — New `BINDERY_LIVE=1` integration test exercises 25 zippoking-reported ISBNs (the three from #284 plus the twelve in #667/#608) plus 50 deterministic random samples against the real DNB SRU endpoint. Asserts each lookup returns a title with no U+0098/U+009C residue and a resolvable author. Wired into a separate nightly workflow so DNB upstream flakiness doesn't block PR merges.
- **`stripMARCNonSortingBrackets` + `extractAuthor` helpers** — DNB title cleanup and author resolution are now isolated for direct unit testing.
- **AddBook orphan-cleanup deferral** — A pointer-tracked flag arms a `defer` that deletes the author row if the book write didn't land. Idempotent: skipped when the author already had books before this AddBook call.

## [v1.11.1] — 2026-05-15

### Fixed

- **NZB Finder indexer searches no longer return Cloudflare 403** — nzbfinder.ws runs a WAF rule that case-sensitively rejects any User-Agent containing the substring `Bindery`. Every search through that indexer was returning HTTP 403 "Attention Required" instead of results. Bindery now sends a single canonical, lowercase User-Agent (`bindery/<version> (<os>)`) on every outbound HTTP request, matching the convention used by Sonarr/Radarr/Prowlarr. Other indexers (NZBGeek, NZB Planet) are unaffected.

### Internal

- **Single source of truth for User-Agent** — New `internal/useragent` package centralises the outbound User-Agent. Twelve HTTP clients (newznab indexers, OpenLibrary, DNB, Hardcover, Audnex, Audible, Google Books, Discord notifier, image proxy, Prowlarr, telemetry, Audiobookshelf, Grimmory) now share one identity. Previously some sent `Bindery/0.1`, some `Bindery/1.0`, three sent Go's default `Go-http-client/2.0` (which is on Cloudflare's bot blocklist), and existing helpers used their own format. All converged on `bindery/<version> (<os>)`.

## [v1.11.0] — 2026-05-14

### Added

- **SSO-only mode and OIDC account controls** (#654) — Three new env vars: `BINDERY_LOCAL_AUTH_ENABLED` (default `true`) disables password login entirely when set to `false`; `BINDERY_OIDC_AUTO_PROVISION` (default `true`) prevents automatic account creation for unknown OIDC users when set to `false`; `BINDERY_OIDC_EMAIL_LINK` (default `false`) links an unknown OIDC identity to an existing account by email on first login. Deployments that don't set these vars are unaffected.

- **Indexer priority now applied to release scoring** (#656) — The `Priority` field on indexers (Settings → Indexers) previously had no effect. It now adds directly to the composite release score, so a higher-priority indexer wins ties and can outweigh small quality differences. Set Usenet indexers to a higher priority than torrent indexers to prefer Usenet when both have matching releases.

### Fixed

- **Recommendations now return empty genre arrays instead of null** — Recommendation storage normalizes missing and legacy `null` genre values to `[]`, keeping API responses consistent for clients.

- **qBittorrent Docker hostname fix** (#640) — Bindery now fetches .torrent content itself and submits it to qBittorrent via multipart/form-data. Fixes setups where qBittorrent runs in a separate container and cannot resolve the Prowlarr/indexer internal hostname (e.g. `prowlarr:9696`). No config change needed.

- **NZBGet Docker hostname fix** (#531) — Bindery now fetches the NZB file itself and sends it to NZBGet as base64-encoded content. Same fix as #640 for Usenet: NZBGet in a separate container no longer needs to reach the indexer URL directly. No config change needed.

- **Non-standard indexer category passthrough** (#636) — Category IDs outside the standard 7xxx/3xxx ranges (e.g. MaM-style IDs) are now forwarded to the indexer unchanged instead of being dropped. Fixes searches returning no results on niche indexers with custom category numbering.

- **Backup delete button in Settings → General → Backup** (#638) — Backup entries now have a delete button in the UI. Previously backups could only be removed by SSHing into the container.

- **Log level toggle now propagates to log viewer** (#651) — Switching to DEBUG in Settings → System now immediately affects the log viewer. Previously only `BINDERY_LOG_LEVEL=debug` at startup had any visible effect.

- **Discover page no longer crashes on books with no genre metadata** (#645) — The Recommendations page handles `genres: null` from the API gracefully instead of throwing a runtime render error.

### Improved

- **Book list query performance** (#648) — Book list queries now use a single CTE JOIN instead of correlated subqueries (previously 2N SQLite queries for N books). Noticeable speedup for libraries with more than a few hundred books.

- **Download client form shows inline errors on save failure** (#649) — The Save button in the Add/Edit Download Client forms now displays an inline error message when the API call fails, rather than appearing to do nothing.

### Internal

- **Test coverage for grimmory and hardcoverlistsyncer** (#652) — Added unit and HTTP test coverage for `internal/grimmory` (0% → 93%) and `internal/hardcoverlistsyncer` (13% → 20%).

### Chores

- **Local check cleanup** — Restored downloader lint compliance and aligned the WantedPage test with optimistic unmonitor behavior so local checks can pass.

## [v1.10.0] — 2026-05-13

### Added

- **Discord stats voice channels** — A k8s CronJob in `deploy/discord-stats.yaml` updates three Discord voice channels every 10 minutes with live active-install count, latest released version, and GitHub star count. Powered by a new `/stats.json` JSON endpoint on the telemetry server. Setup steps in `deploy/README.md`.
- **Live ISBN provider integration tests** — New torture-corpus tests (`BINDERY_INTEGRATION=1` to run) exercise the aggregator's ISBN fallback chain against real DNB / OpenLibrary / GoogleBooks / Hardcover endpoints. Useful for catching upstream schema drift; skipped by default to avoid CI flake. Extracted from #515.
- **Library scan status auto-refresh** (#544) — The Settings page now polls the scan endpoint every 2 s while a scan is running and shows an inline progress banner. No more F5 to see when the scan finishes.
- **Expanded frontend test coverage** (#485) — Auth flow, Login page, History page, Queue page, WantedPage, full SettingsPage suite, and MSW-based API client tests; previously untested paths now have coverage.
- **Auto-bump `bindery-ping` on release** (#557) — The goreleaser CI job now updates the `LATEST_VERSION` env var on the bindery-ping deployment automatically, eliminating the manual `auto/prod-deploy-X.Y.Z` step after each release. Requires `HETZ1_KUBECONFIG` secret.

### Fixed

- **Stale ABS-sourced author aliases are now cleaned up post-import** — When an audiobook import recorded a co-author (or a different-named primary author) as an alias, the alias would stick around tagged `SourceOLID="abs"` even when it no longer matched the canonical author. The importer now sweeps these at the end of each run and drops aliases that don't fuzzy-match the canonical name. Also prevents pen-name corruption by requiring secondary-author aliases recorded during import to fuzzy-match the canonical author. Extracted from #515.
- **Manual alias deletion** — New `DELETE /api/v1/author/{id}/aliases/{aliasID}` endpoint lets the UI / API clients remove specific aliases without merging the whole author. Scoped to (authorID, aliasID) pair; returns 404 if the alias isn't on that author so cross-author tampering can't happen. Extracted from #515.
- **Download client error messages now suggest the root cause** (#621) — Test-connection failures are classified by errno: DNS resolution failure suggests checking container networking; connection refused suggests the service isn't running on that port; timeouts suggest a firewall or proxy.
- **WantedPage optimistic updates now roll back on failure** (#551) — If the API call fails after a monitored/wanted toggle, the UI reverts to the previous state and shows a toast ("Couldn't update — reverted. Retry?") instead of silently diverging from server state.
- **Graceful SIGTERM/SIGINT shutdown** (#559) — The server now drains in-flight requests before exiting. Grace period defaults to 30 s and is configurable via `BINDERY_SHUTDOWN_GRACE`. `kubectl rollout restart` no longer drops in-flight requests.

## [v1.9.3] — 2026-05-12

### Fixed

- **DNB add-by-ISBN no longer fails for German books with no OpenLibrary coverage** (#608) — DNB results now carry a stable author ForeignID (GND from MARC 100 $0 when present, otherwise a synthetic `dnb:author:<name-slug>`). When a canonical author (OpenLibrary / Hardcover) later arrives for the same SortName, the synthetic DNB row is migrated in place so the user keeps a single author record. Previously the add flow returned 422 "Author metadata unavailable" whenever no canonical provider had the ISBN.

## [v1.9.2] — 2026-05-12

### Fixed

- **Backup creation no longer crashes the Settings page** (#594) — The backup endpoint returns `{name, size, modTime}` but the frontend was typed for `{filename}` and rendered the raw object, triggering React error #31 ("Objects are not valid as a React child"). Frontend types and the backup list rendering now match the API response shape.

- **Hardcover list sync** (#562) — `GetListBooks` now queries the plural `lists(where: {id: {_eq: $id}}, limit: 1)` root field. The singular `list(id:)` query was rejected by Hardcover's GraphQL schema ("field 'list' not found in type: 'query_root'"), breaking every custom list Sync Now since v1.1.0.

- **Proxy auth `/api/v1/auth/status`** (#560) — Proxy identity resolution now runs before the allow-unauth fast-path. Previously `/auth/status` was on the allow-unauth list and bypassed the proxy header lookup entirely, so SSO-authed users were never let past the login screen. Trusted-proxy CIDR gating preserved.

- **Newznab / Prowlarr-proxy NZBGet rejections** (#531) — Download enclosure URLs are now signed with the indexer apikey when the URL host matches the indexer's own host. Prowlarr-proxied Usenet downloads were arriving at NZBGet as empty content ("Document is empty" / `id 0`) because the apikey was stripped at client construction but never re-applied to download URLs. Apikey is only appended for same-host URLs to avoid leaking it to third-party redirect targets.

- **Author matching false positives** (#563) — Indexer release filter and library scanner now require all significant author tokens (>=3 chars) to match at word boundaries, not just surname substring. Releases like `Adam.Reid.Book.epub` will no longer be auto-imported under monitored author "Rachel Reid". Initials (1-2 char tokens) are treated as optional, so "George R. R. Martin" still matches "George Martin". ABS path was already safe (Jaro-Winkler whole-string match).

- **Enhanced Hardcover series controls no longer hidden by default** (#596) — The deployment-wide `BINDERY_ENHANCED_HARDCOVER_API` flag now defaults to enabled. The saved Hardcover token and **Settings → General** admin toggle remain as the normal feature gates. Operators can still set the flag to `false` to disable the feature for an entire deployment.

- **ABS review search results are scrollable and keep book-author links intact** (#599) — No-match review author/book searches now show up to 10 scrollable matches instead of truncating after three, and selecting a book result auto-links its author before resolving the book when the review item does not already have a resolved author.

### Added

- **Download queue surfaces timestamps** (#543, #592) — Each queue entry now shows the most recent meaningful event (imported / completed / grabbed / added) as a relative time, with the absolute UTC timestamp on hover.

## [v1.9.1] — 2026-05-11

### Fixed

- **Author list no longer hides authors after user re-creation** — The "author already exists" duplicate check was global-scoped while the author list filtered by `owner_user_id`. Authors whose `owner_user_id` pointed to a deleted or re-created user were permanently invisible in the list but blocked re-addition. The check is now user-scoped so it agrees with what the list shows. A new migration (039) resets orphaned `owner_user_id` values to NULL so those authors become visible to all users immediately on upgrade.

- **Canonical author name search now scoped to current user** — The name-deduplication path during author creation previously searched the global author pool, which could conflict with authors belonging to other users in multi-user setups.

### Chores

- **Frontend regression coverage expanded** (#427) — Added MSW-backed tests for login, CSRF handling, auth state/guards, Book Detail search/grab flows, and Wanted page search/grab/bulk actions.

## [v1.9.0] — 2026-05-11

### Added

- **Book metadata can be remapped from the Book Detail page** (#590) — Books with ABS or stale metadata now show an **Improve metadata** action that searches upstream providers or accepts a direct provider ID. New `POST /api/v1/book/{id}/map` applies the upstream title, cover, language, ratings, genres, and provider ID while preserving local status, files, media type, ASIN, narrator, selected edition, and exclusion state.

- **Calibre-Web-Automated (CWA) ingest** (#417) — A new
  **Settings → General → Calibre-Web-Automated (CWA)** field configures a
  shared ingest folder. When set, every successful ebook import is also
  copied into that folder so a sibling
  [CWA](https://github.com/crocodilestick/Calibre-Web-Automated) container
  can auto-ingest it. Bindery keeps its own copy and never moves the file,
  so a misconfigured CWA can't take bindery's library with it. No Calibre
  runtime dependency is added to the bindery container — only the file
  drop is in scope. Audiobook imports are unaffected since CWA is built
  around ebook libraries.

- **Prowlarr search timeout is now configurable** (#576) — The Prowlarr indexer
  search timeout has been raised from 15 s to 60 s and can be adjusted in
  **Settings → Indexers → Prowlarr → Search timeout**. Slow usenet indexers
  no longer time out on the first query.

### Fixed

**Importer / download clients**

- **qBittorrent SavePath fallback caused incorrect imports** (#574) — When
  qBittorrent's `content_path` field was absent or empty, the importer
  fell back to `SavePath` (the shared download root) and could match
  unrelated files or walk directories it should not touch. The importer
  now uses `content_path` exclusively and aborts cleanly when it is missing.
- **Default import mode changed from `move` to `hardlink`/`copy`** (#577) —
  The out-of-box default was `move`, which silently broke torrent/usenet
  seeding immediately after import. Bindery now defaults to `hardlink` when
  source and destination are on the same filesystem (free, preserves seeding)
  or `copy` when they are cross-device. **Upgrade note**: migration 038
  clears the implicit `move` default written at install time; users who
  explicitly set an import mode in Settings are not affected.
- **Downloads stuck in `importFailed` are now retried automatically** (#578)
  — Previously, a download that failed during import was permanently orphaned.
  Bindery now retries up to three times before leaving it for manual
  intervention. Retry count is persisted via migration 037.
- **CheckDownloads now polls all enabled download clients** (#572) — Only
  the highest-priority client was polled for status updates. Secondary
  clients (e.g. a second qBittorrent instance or a fallover) were silently
  ignored. All enabled clients are now iterated in priority order.
- **Bulk-grab torrent dedup race condition fixed** (#573) — Grabbing multiple
  releases simultaneously could assign the same `torrent_id` to two
  downloads, breaking per-download tracking. `AddTorrent` is now serialised.

**Auth**

- **API key authentication now grants admin role** (#582) — Requests
  authenticated via API key successfully verified the key but did not set
  the admin role in the request context, causing `RequireAdmin`-protected
  endpoints to return 403. The role is now correctly propagated.
- **Auth endpoints no longer require `X-Requested-With` header** (#575) —
  The login endpoint enforced `X-Requested-With: bindery-ui`, blocking
  non-browser clients (curl, mobile apps, integrations). Auth endpoints are
  now exempt; programmatic clients should use API key auth instead of cookie
  sessions.

**AudioBookShelf (ABS)**

- **ABS library is rescanned after audiobook import** (#581) — Bindery now
  triggers `POST /api/v2/libraries/:id/scan` after a successful audiobook
  import so the file appears in ABS immediately rather than on its next
  scheduled scan.
- **Move-mode audiobook imports no longer appear MISSING in ABS** (#583) —
  The ABS rescan after import updates ABS's path knowledge, resolving the
  MISSING status that appeared when the import moved the file.
- **History events include format for dual-format books** (#584) — `bookImported`
  events for books with `media_type='both'` now record which format (ebook
  or audiobook) was imported, making the History page unambiguous.

**Metadata**

- **ISBN lookups now canonicalise provider-native matches** (#590) — ISBN
  searches normalise ISBN input, consult configured metadata enrichers, and
  conservatively relink provider-native results back to canonical OpenLibrary
  works when the author/title evidence is unambiguous. This improves
  translated and edition-specific matches while avoiding plausible
  wrong-title fallbacks.
- **Audiobook ASIN enrichment can relink to upstream metadata** (#590) —
  Enriching an audiobook now uses Audnex ASIN metadata to find a safe
  canonical upstream match, so ABS/imported audiobook rows can gain better
  titles, covers, language, search metadata, and OpenLibrary IDs while
  keeping audiobook-specific fields intact.
- **ABS imports no longer trust stale secondary-author aliases or provenance**
  (#590) — Existing ABS author provenance and aliases are reused only when
  they still match the local author, preventing secondary-author names from
  corrupting future imports.
- **Direct book adds preserve series links** (#590) — Adding a book directly
  no longer drops existing series associations during metadata
  canonicalization.
- **Google Books provider settings are respected at startup** (#590) —
  Bindery now prefers the UI-managed Google Books API key, keeps legacy
  setting fallback for existing installs, and treats a deliberately cleared
  UI setting as disabled.

## [v1.8.1] — 2026-05-09

### Fixed

- **DNB search results couldn't be added to the wanted list** (#545, #561)
  — DNB bib records expose author *names* but not author IDs, so every
  result had the Add button greyed out with a misleading "try a more
  specific search" hint. The fix extracts ISBN(s) from MARC 020 in DNB
  records and adds a cross-provider author resolver: when a search result
  lacks a foreign author ID, the backend looks up the ISBN in OpenLibrary
  and rewrites the request to use OL's canonical author/book identity.
  Books that resolve end up under their OpenLibrary record (with OL's
  title and metadata); books with no OL match return a clear "add the
  author manually first" error instead of silently failing.
- **Telemetry chart hides the freshly cut release** (#546) — `/stats`
  truncated the version chart to top-8 by count, so a brand-new release
  with one or two installs disappeared into `(other)` until it
  organically out-ranked older versions (sometimes weeks). The chart now
  pins the configured `LATEST_VERSION` into the visible region and
  annotates it `(latest)` so newly cut releases are immediately visible.
- **Transmission retry path silently used corrupted bodies** (#558) — On
  retry against the Transmission RPC endpoint, `io.ReadAll` errors were
  dropped and an empty / partial slice was used as the response body.
  Errors now propagate via `fmt.Errorf("transmission: read retry body:
  %w", err)` so a torn body fails loudly instead of producing
  garbage-decoded torrent state.
- **`refreshBookStatus` could zero a user's file paths on transient DB
  errors** (#558) — `Scan` errors on the `book_files` lookup were dropped
  via `_ = QueryRowContext(...).Scan(&path)`, so any error other than
  `sql.ErrNoRows` (lock timeout, corruption, connection drop) wrote `""`
  back to `book.EbookFilePath` / `book.AudiobookFilePath`. Now distinguishes
  `sql.ErrNoRows` (legitimate empty path) from real failures via
  `errors.Is`, returning the wrapped error in the latter case.
- **Non-ASCII filenames mangled in Content-Disposition** (#558) — Library
  file downloads now emit RFC 5987 `filename*=UTF-8''<percent-encoded>`
  alongside the legacy `filename="..."` parameter, so titles with German /
  Cyrillic / CJK characters land on disk with the correct name instead of
  being rewritten to a quoted-printable mojibake form.
- **Frontend timer leaks on unmount** (#556) — `AuthSettings`'s
  copy-to-clipboard "copied" badge and `DiscoverPage`'s toast clear-out
  used bare `setTimeout` calls that fired against unmounted components,
  producing React's "can't update state on unmounted component" warning.
  Both are now `useEffect`-driven with `clearTimeout` cleanup.

### Changed

- **Log persistence shutdown is now graceful** (#558) — `LogHandler.Stop()`
  closes the in-memory channel and waits (bounded by a 5s context) for
  the drain goroutine to flush any in-flight log entries before the
  process exits. Wired into `cmd/bindery/main.go` as a deferred call.
  Previously the goroutine leaked for the lifetime of the process and
  any buffered entries were lost on shutdown. Note: the `defer` only
  fires on clean main-return paths, not on signal-driven termination —
  full benefit requires #559 (signal-based graceful shutdown), tracked
  separately.
- **Several previously-dropped errors now surface in the log stream**
  (#558) — `slog.Warn` calls were added to `internal/api/imageproxy.go`
  (response write), `internal/api/auth_oidc.go` (provider parse),
  `internal/api/authors.go` (batch dedup updates),
  `internal/db/recommendations.go` (genres marshal), and
  `internal/prowlarr/syncer.go` (`SetLastSyncAt` after a successful
  sync). Behaviour is unchanged; visibility is not.

### Security

- **API key compared with `subtle.ConstantTimeCompare` instead of `==`**
  (#555) — Both the main HTTP middleware (`internal/auth/middleware.go`)
  and the OPDS auth path (`internal/api/opds_auth.go`) used variable-time
  string equality on the API key, leaking enough timing information to
  enable a remote byte-by-byte recovery attack against a sufficiently
  determined attacker. Both sites now use `subtle.ConstantTimeCompare`,
  with the existing empty-key short-circuit preserved so empty submissions
  don't telegraph the real key's length.
- **GitHub Actions in `ping-server.yml` pinned to commit SHAs** (#555) —
  Five actions (`actions/checkout`, `docker/login-action`,
  `docker/setup-qemu-action`, `docker/setup-buildx-action`,
  `docker/build-push-action`) were pinned by tag, leaving the workflow
  vulnerable to a tag-rotation supply-chain attack. All are now pinned to
  the same commit SHAs already in use in `ci.yml`.
- **`cmd/telemetry-server/Dockerfile` base images pinned by digest**
  (#555) — `golang:1.25-alpine` and `alpine:3.21` are now pinned to their
  content-addressable digests so a tag rotation can't silently swap the
  base image during the next ping-server build.
- **Dedicated `*http.Client` for telemetry pings** (#555) — Replaces
  `http.DefaultClient` for the once-per-day telemetry ping path with a
  package-local client carrying its own timeout. The 10s context deadline
  was already in place, but the dedicated client guards against unrelated
  code mutating `DefaultClient` and reaching into the ping path.

## [v1.8.0] — 2026-05-09

### Added

- **Manual Hardcover list sync** (#536) — A "Sync now" button on each Hardcover list row in **Settings → Import** triggers an immediate sync without waiting for the 24-hour scheduler tick. New `POST /api/v1/importlist/{id}/sync` endpoint backs the button and is scriptable from the CLI.
- **Top-level React ErrorBoundary** (#530, #539) — Render-time errors no longer blank the entire page. A friendly fallback card with **Reload** / **Show details** buttons sits outside the router, so even router-level throws are caught.

### Fixed

- **Prowlarr add-form silently swallowed errors** (#536) — Failed adds now surface a red error message under the form instead of failing silently. Sync errors (separate from the add itself) are non-fatal so a successful Prowlarr connection is not rolled back by a transient sync failure.
- **Telemetry only pings for semver release versions** (#527) — Dev / branch builds no longer ping the telemetry endpoint, keeping ingestion data clean.

### Security

- **Go 1.26.3 stdlib security release** (#540) — Bumps the runtime image from `golang:1.26.2-alpine` to `1.26.3-alpine`, picking up patches for CVE-2026-42499, CVE-2026-39820, CVE-2026-39823, CVE-2026-39825, CVE-2026-39826, CVE-2026-33811, CVE-2026-33814, CVE-2026-39836. Container Scan (Trivy CRITICAL+HIGH) returns to green on this release.
- **`security-events: write` scoped to SARIF-uploading jobs only** (#538) — Removed the over-broad workflow-level write permission from `security.yml`; only the four jobs that actually call `github/codeql-action/upload-sarif` (`sast-go`, `sast-frontend`, `secrets-scan`, `iac-scan`) hold the scope. OpenSSF Scorecard `Token-Permissions` improvement.
- **Dependabot security updates enabled** at the repo level — weekly version updates already shipped via `dependabot.yml`; this turns on the security advisory channel for transitive vulns.

### Docs

- **Indexer / Prowlarr URL guidance** (#536) — New section in `docs/DEPLOYMENT.md` explaining why loopback URLs (`127.0.0.1`, `localhost`) are rejected by the SSRF policy and what alternatives to use (docker service name, LAN IP, or container IP).
- **README pruned to ~280 lines** — Hero, Why Bindery, Features (compressed), Quick Start, signposts. Implementation detail moved to new `docs/ARCHITECTURE.md` and `docs/API.md`. SECURITY.md supported-versions table bumped to 1.8.x.
- **Unraid Community Apps template** (#526) — Template added to repo; selfhosters marketplace listing pending review.

### Chores

- **Series Codecov follow-up coverage** (#475) — Targeted tests for series API edge cases, repository hydration and linking behavior, metadata aggregator series catalog fallback/cache behavior, and series matching helpers after gaps were noticed in the Codecov report for PR #459.
- **Hero screenshots refreshed** (#528, #529).

## [v1.7.0] — 2026-05-08

### Added

- **Subpath / reverse-proxy hosting** (`BINDERY_URL_BASE`) (#516) — New env var strips incoming URLs to their path, validates the prefix, injects `<base href>` and `window.__BINDERY_BASE__` into the served `index.html` at runtime, and mounts all chi routes under the prefix. Vite is built with `base: './'` for relative asset URLs; the React router and API client read `window.__BINDERY_BASE__` for the basename and prefix.
- **Re-bind book to a different metadata record** (#519) — New `POST /api/v1/books/{id}/rebind` endpoint accepts a provider (`openlibrary` | `hardcover`) and a foreign ID, validates the upstream record, warns on author mismatch (with `force_required:true`) unless `force:true` is sent, clears and re-links series membership, and writes a `bookRebound` audit entry to History. A Re-bind dialog is accessible from the Book Detail page.
- **DNB as primary metadata provider** (#521) — DNB's SRU endpoint now supports `GetAuthorWorks()` and can be selected as the primary provider in **Settings → General → Metadata Provider**. OpenLibrary remains the default. When DNB is primary, roles are swapped at startup.

### Fixed

- **Library scan: sort-suffix folders now reconciled** (#517) — Files stored in librarian sort-suffix form (`Title, The` / `Title, A`) are correctly matched during library scan. A new `normalizeTitle()` helper handles `, the` / `, an` / `, a` comma-suffix inversion and is applied in both `titleMatch()` and the JW-similarity comparison in `ScanLibrary()`.
- **Hardcover lists fetch repaired** (#518) — Three response structs (`GetUserWishlist`, `GetUserLists`, `getShelfBooks`) incorrectly expected a single `Me` object; they now unmarshal `Me` as an array with a `len==0` guard, matching the actual API response shape.

### Changed / Refactored

- **Post-create wanted-book logic centralised** (#520) — `handleNewWantedBook()` extracted in `internal/api/authors.go` and called from `FetchAuthorBooks`, `RecommendationHandler.Add`, and `SeriesHandler.ensureHardcoverCatalogBook`, eliminating three copies of the same file-exists / auto-search dance.

## [v1.6.0] — 2026-05-07

### Fixed

- **Authenticated users no longer see the login page** (#493) — Visiting `/login` (or `/setup`) with a valid session now redirects to `/` instead of rendering a stale auth screen. New `PublicOnlyRoute` guard wraps both routes; mirrors the existing `AuthGuard` loading behaviour so there is no flash on refresh, and routes back to `/setup` if setup is still required.
- **Discover page now uses the wrapping-grid layout** (#347) — Recommendation rows now match Books and Authors with `grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4` so all cards are visible at a glance, instead of horizontally scrolling. Section headings, grouping logic, the cold-start note, and empty/disabled states are unchanged.

## [v1.5.0] — 2026-05-07

### Added

- **Audiobookshelf (ABS) integration is now always enabled** — The `BINDERY_ABS_ENABLED` feature flag has been removed. ABS configuration, import, review, and conflict endpoints are unconditionally available; the ABS tab always appears in Settings for admins. Existing installs that relied on the flag being `false` to hide the UI will now see the tab; those that set it to `true` can simply remove the env var.
- **Grimmory integration** — New **Settings → Grimmory** tab for configuring a [Grimmory](https://grimmory.org/) self-hosted digital library. Stores server URL and API key; a Test Connection button pings `GET /api/status`. New `GET/PUT /api/v1/grimmory/config` and `POST /api/v1/grimmory/test` endpoints back the UI. API paths are based on current Grimmory OpenAPI docs and will be updated as the API stabilises.
- **Separate audiobook download watch folder** — New `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` env var (also exposed in **Settings → General**). When set, the scanner uses this directory for audiobook downloads and falls back to `BINDERY_DOWNLOAD_DIR` for ebooks. Unset by default — fully backwards-compatible. Mirrors the existing `BINDERY_AUDIOBOOK_DIR` split on the library side.

### Fixed

- **`.opus` added to recognised book and audio-tag extensions** — Opus-encoded audiobook files are now detected and tagged correctly.
- **Hardcover built-in shelves surface in the list picker** — "Want to Read", "Currently Reading", and "Read" now appear alongside user-created lists when adding a book to a Hardcover shelf.
- **Telemetry security hardening** (#482) — Three fixes to the optional telemetry server: redirect target now uses `CANONICAL_HOST` instead of the user-controlled `Host` header (open redirect); rate-limiter key now strips the port from `RemoteAddr` so connections from the same IP share one bucket; `BINDERY_TELEMETRY_DISABLED=true` is now checked before the settings table on first boot so the opt-out takes effect immediately.

## [v1.4.5] — 2026-05-06

### Added

- **Book Detail page now exposes the media-type selector** — Imported and downloaded books can now be flipped between ebook / audiobook / both directly from the Book Detail page. Previously this was only available on the Wanted page, so once a book progressed past wanted there was no UI path to add the second format short of deleting and re-adding the author.
- **Author Detail page now has an Edit modal** — Quality profile, metadata profile, and root folder are now editable from the Author Detail page; previously the only way to change them was to delete the author and re-add. Triggered from a new Edit button next to the existing actions.

### Fixed

- **Mobile session cookie no longer evicted on app switch** — Login without "Remember me" now sets `Max-Age` on the session cookie (was previously a browser-session cookie with no expiry hint), so iOS Safari and Android Chrome don't drop it when the tab is backgrounded or the OS suspends the browser process. The 12-hour / 30-day durations were already encoded in `auth.SessionDurationShort` / `auth.SessionDuration` but never reached the wire on the short branch.

## [v1.4.4] — 2026-05-06

### Fixed

- **Manual series mutations require admin role** (#468) — Authenticated non-admin users can no longer create, update, monitor, delete, fill, or link series to Hardcover. Read-only series endpoints remain available to authenticated users.
- **Library file matching now respects media type** (#488, #454) — `FindExisting` now picks the right library root based on the book's `media_type` instead of always walking `BINDERY_LIBRARY_DIR` first. Audiobook book rows are matched against `BINDERY_AUDIOBOOK_DIR` (with fallback to `BINDERY_LIBRARY_DIR` when the audiobook root is unset), ebook rows are matched against `BINDERY_LIBRARY_DIR`, and dual-format / unspecified rows preserve the prior behaviour of walking both roots with the ebook library first. Previously a same-titled ebook in `libraryDir` could be mis-attributed to an audiobook entry on rescan, and authors filtered to "audiobooks only" still had file lookups walk the ebook root.
- **Edition dedup now strips subtitles** (#458) — Author sync no longer creates duplicate rows when OpenLibrary returns the same work twice with different subtitle handling — typically the audiobook drops the post-colon subtitle while the ebook keeps it (e.g. *Carl's Doomsday Scenario* vs. *Carl's Doomsday Scenario: Dungeon Crawler Carl, Book 2*). `NormalizeTitleForDedup` now drops a `: subtitle` tail when the colon is followed by whitespace, so both editions collapse to the same key and the existing v1.3.1 dual-format upgrade path is taken instead of inserting a duplicate.
- **Series title inputs now have an API length limit** (#469) — Manual series creation and title updates now reject titles longer than 500 bytes before writing to SQLite, preventing oversized titles from being stored through the HTTP API.
- **Hardcover GraphQL success responses are bounded** (#470) — Successful Hardcover responses are now read through an 8 MiB cap so a misbehaving upstream cannot force unbounded memory growth before JSON parsing.
- **Add-author search no longer hides valid author results when the query matches a book title** — Results whose name exactly matches a known book title and whose disambiguation points to that book's real author are now placed behind a reveal button rather than silently dropped.

## [v1.4.3] — 2026-05-06

### Fixed

- **Discover now shows recommendations for all libraries** — The `ratingsCount < 50` hard filter was silently dropping every candidate from the most useful recommendation sources: monitored-author books, series continuations, and genre-popular picks from OpenLibrary. These sources already carry an implicit quality signal (the user chose to monitor the author; the book is part of a series they're reading; OL's subject curators selected it), so gating them on OL's sparse ratings data was wrong. Only serendipity and list-cross candidates — which come from broader, uncurated pools — now require a ratings signal. The filter is unchanged for those types.

## [v1.4.2] — 2026-05-06

### Fixed

- **Discover works for libraries migrated from pre-v1.4.1** — Author refresh now updates `ratings_count` and `average_rating` on books that already exist in the database. Previously, `FetchAuthorBooks` skipped all processing for existing books (by foreign ID or deduplicated title), so libraries that had synced authors before v1.4.1 kept `ratings_count=0` on every book even after an upgrade and refresh. The recommender's hard filter then dropped all candidates, leaving Discover empty. A refresh-metadata run now back-fills ratings from OpenLibrary for any book where we have better data.

## [v1.4.1] — 2026-05-06

### Fixed

- **Discover page no longer shows "not enough data" for populated libraries** — `GetAuthorWorks` was not requesting `ratings_count`/`ratings_average` from the OpenLibrary search API, so every book stored via a monitored-author sync had `RatingsCount=0`. The recommender's hard filter drops candidates with fewer than 50 ratings, silently eliminating all author-new and series candidates regardless of library size. The search query now fetches those fields and the enrichment merge propagates them to the stored book rows.

## [v1.4.0] — 2026-05-05

### Added

- **Enhanced series data via Hardcover** — Series can now be managed manually, linked to Hardcover series, and compared against the Hardcover catalog. The Series page shows present, missing, local-only, and uncertain books; missing catalog entries can be filled all at once or one row at a time, creating wanted/monitored book rows and queuing searches. The enhanced controls are gated behind `BINDERY_ENHANCED_HARDCOVER_API`, a saved Hardcover API token, and the admin setting in **Settings -> General**.
- **Prometheus `/metrics` endpoint** (#429) — Bindery now exposes `bindery_http_*` (request rate / latency by route template), `bindery_scheduler_*` (job-run counts and durations), and `bindery_build_info` alongside the standard `go_*` runtime and `process_*` collectors. Mounted at `/metrics` outside the `/api` auth chain so Prometheus scrapes work without session cookies; restrict access via NetworkPolicy / firewall / reverse-proxy ACL. Background jobs now also recover from panics so a single buggy job no longer tears down the scheduler goroutine.
- **OIDC settings UI gains a "Test discovery" button** (#460) — Next to the Issuer URL field on the **Add provider** form, a Test button hits the IdP's `/.well-known/openid-configuration` server-side and renders the result inline: discovered authorize/token endpoints + supported scopes on success, the raw error (DNS, TLS, 404, JSON parse) on failure. Critically, surfaces **issuer mismatch** when the discovered issuer differs from the entered URL — the silent killer for Authentik per-provider mode and Keycloak realm paths. New `POST /api/v1/auth/oidc/test-discovery` endpoint backs the button.
- **OIDC settings UI shows a live callback URL preview** (#460) — As you type the provider id in **Settings → Security → OIDC Providers → Add provider**, the form renders the exact redirect URI Bindery will register with the IdP, with a copy-to-clipboard button. New `GET /api/v1/auth/oidc/redirect-base` endpoint returns `{ base, callback_path }` for the current request — eliminates the most common setup mistake (registering a URL that doesn't match what Bindery actually sends).

### Changed

- **OIDC redirect base URL is now optional behind a trusted proxy** (#460) — `BINDERY_OIDC_REDIRECT_BASE_URL` is no longer strictly required when Bindery sits behind a reverse proxy. If the env var is unset and `BINDERY_TRUSTED_PROXY` is configured, Bindery derives the public-facing base URL from `X-Forwarded-Proto` + `X-Forwarded-Host` on each request. Explicit env-var values still win when set (needed for path-prefix deploys). Previously a missing env var produced a relative `redirect_uri`, which IdPs reject with `redirect_uri_mismatch`. The redirect base resolved at `/login` is round-tripped through the flow cookie so `/callback` uses the same value during the token exchange.

### Fixed

- **OIDC providers no longer silently dropped after failed startup discovery** (#461) — Providers whose discovery fails during `Reload()` are now tracked in a separate failed-providers map instead of being silently logged-and-forgotten. `GET /api/v1/auth/oidc/providers` returns a per-provider `status` block (`"ok"` / `"failed"` with the last error and timestamp) so admins can diagnose without grepping logs. The first login attempt for a failed provider triggers an on-demand re-discovery (rate-limited to once per 30s), so transient startup failures (e.g. pod recreated before IdP is reachable) recover automatically without an admin restart.
- **ABS imports require saved source configuration** — import and dry-run starts now use only the stored ABS configuration, and the UI blocks runs while ABS settings contain unsaved changes so previews and imports cannot run against one-off request overrides.
- **Hardcover auto-linking requires local evidence** — automatic series linking now requires local book overlap or author agreement before accepting a high-confidence Hardcover candidate, and missing-book fill skips books that already exist as excluded titles.

### Docs

- Added user-facing Hardcover series wiki documentation and documented the enhanced Hardcover series migration, feature flag, token requirement, admin toggle, and production network expectations in the deployment guide.

## [v1.3.1] — 2026-05-05

### Fixed

- **Possessive author prefix stripped before release matching** (#446) — Search results for titles like *Tom Clancy's Rainbow Six* no longer require the release to carry "Clancy's"; the possessive prefix is stripped before keyword extraction. Handles both ASCII apostrophe and Unicode right-single-quotation-mark (U+2019).
- **Readarr import returns structured error on failure** (#447) — The Readarr DB import handler now returns a JSON `{"error": "…"}` body with an appropriate HTTP status on failure instead of an empty 500.
- **Edition deduplication upgrades existing row to dual-format** (#448) — When OpenLibrary returns both an ebook Work and an audiobook Work for the same title during an author sync, Bindery now upgrades the existing book row to `media_type: both` instead of inserting a duplicate entry.
- **Library scanner searches both library and audiobook roots** (#456) — `FindExisting` now walks `BINDERY_AUDIOBOOK_DIR` alongside `BINDERY_LIBRARY_DIR` when checking for pre-existing files, and pre-filters by author folder to prevent cross-author mismatches. Previously only the ebook library was checked, leaving audiobook files undetected on rescan.
- **Download client edge-case coverage** (#431) — Added hermetic matrix tests for RemoteID normalization, live status error mapping, poll failures, unreachable clients, context deadlines, and qBittorrent unfiltered hash polling. Transmission queue overlays now surface non-empty `errorString` values as error statuses.

## [v1.3.0] — 2026-05-05

### Added

- **Audiobookshelf (ABS) import workflow** (#371) — Bindery can now connect to one ABS source, validate an API key, discover visible book libraries, and import ABS catalog metadata into shared authors, books, series, and ebook/audiobook editions. Imports support dry runs, persisted run history, rollback preview/rollback, low-confidence review queues, metadata conflict resolution, and path remaps when ABS and Bindery see the same files under different mount prefixes. Import quality is best when the ABS library already has strong metadata, especially ASIN coverage.

### Changed

- **ABS configuration saves no longer probe the live server** — saving ABS settings now normalizes and stores the base URL, label, enabled flag, selected library ID, path remaps, and write-only API key without contacting ABS. Use **Test connection** or **List libraries** for live validation; library discovery returns book libraries only.
- **ABS importer internals split by domain** — the importer orchestration remains in `internal/abs/importer.go`, with author matching, upserts, file reconciliation, metadata conflicts, rollback, snapshots, shared types, and utilities split into focused helper files.

### Fixed

- **ABS imports reject non-book libraries before scanning** — import enumeration now validates that the selected ABS library page is `mediaType=book` and that each returned item is a book item before mapping catalog data.
- **ABS API calls use a Bindery user agent** — config probes and import enumeration now send `User-Agent: bindery/<version>` (`bindery/dev` when no build version is available) instead of the Go default user agent.
- **Docker image now published for linux/arm64** (#445) — the `image` CI job previously only built `linux/amd64`; both `linux/amd64` and `linux/arm64` are now built and pushed in a single multi-platform manifest, fixing image-pull failures on Apple Silicon and Raspberry Pi hosts.

### Chores

- **Go toolchain pinned for CI** — `go.mod` now targets Go `1.25.9`, while GitHub Actions setup uses the Go `1.25` minor family across CI, security, and ABS contract jobs.

### Docs

- Added an ABS import guide and user-facing wiki documentation covering setup, required API-key access, path remaps, review flow, conflicts, rollback, and import-quality expectations.

## [v1.2.7] — 2026-05-04

### Added

- **Arr-compatible queue endpoint for Harpoon integrations** (#370) — `GET /api/queue` returns a Sonarr/Radarr-style queue payload with `totalRecords`, queue records, live `size`/`sizeleft`, downloader status, client name, remote download ID, protocol, optional pagination, and sorting. The existing `GET /api/v1/queue` UI response remains unchanged.
- **Author detail search-all-wanted action** (#410) — the Author Detail page now has a **Search all wanted** button that queues searches for that author's monitored wanted books, disables itself when there is nothing searchable, and surfaces bulk-search errors inline. Author bulk search now also skips unmonitored wanted books so explicit per-book unmonitor decisions are respected.
- **Startup configuration validation** (#430) — Bindery now validates its configuration on startup and logs actionable warnings for known conflict patterns (conflicting audiobook dir, invalid URLs, non-existent paths). Does not block startup; surfaces problems early before they cause silent failures at runtime.
- **Configurable login rate-limit thresholds** (#428) — `BINDERY_RATE_LIMIT_MAX_FAILURES` (default 5) and `BINDERY_RATE_LIMIT_WINDOW_MINUTES` (default 15) let operators tune the per-IP brute-force lockout without recompiling.

### Fixed

- **OpenLibrary search results restored** (#408) — the deprecated `/search.json` endpoint (which began returning HTTP 500) is replaced by `/authors/{id}/works.json` as the primary works source with `/search` demoted to enrichment. Series data now comes from the primary call so fewer round-trips are needed.
- **Audiobook routing now respects `BINDERY_AUDIOBOOK_DIR`** (#421) — per-author ebook root folders were incorrectly applied to audiobook destinations; audiobooks now always route to the dedicated audiobook directory and ignore the ebook root.
- **Audiobook directory visible in Settings UI** (#420) — the audiobook storage path is now displayed in Settings → General alongside the library directory.
- **API-key requests exempt from CSRF middleware** (#424) — external tools such as Harpoon that authenticate via X-Api-Key header were receiving 403 on `POST /api/queue`; API-key-authenticated requests now bypass `RequireXRequestedWith` and `RequireCSRFToken` checks while browser-session requests remain protected.
- **Torrent hash case sensitivity** (#425) — torrent hashes are now lowercased on assignment, preventing hash-not-found mismatches when clients return mixed-case identifiers.
- **Transmission error states now surface in queue** (#426) — integer status codes 16 and 32 (error / isolated-error) are now recognised and translated to `TrackedDownloadStatus: Warning`, so stuck Transmission downloads appear in the queue instead of silently stalling.
- **CSV author import skips header row** (#419) — CSV imports with a header row no longer create a spurious author entry from column names.
- **qBittorrent hash detection no longer filtered by category** (#418) — the category filter on the hash detection poll was a spurious race condition that prevented hashes from being recorded on redirect URLs; the filter is removed.
- **Credential normalization silent clear fixed** (#422) — `normalizeClientCredentialStorage` now applies the same `legacyCredentialURLBase` guard as the read path, preventing a bare `url_base` with no `api_key` from being silently migrated into `username` on write.
- **Library scanner series matching now runs in production** — the scanner is wired to the series repository at startup; filename-based series/position matching now runs during normal library scans.
- **qBittorrent and Transmission URL Base preserved on read** — legacy credential hydration no longer clears real reverse-proxy URL Base values (e.g. `/qbit`) — only old credential-as-url_base rows are migrated.

## [v1.2.6] — 2026-04-25

### Fixed

- **NZBGet grabs broken** (#396) — `GetFirstEnabledByProtocol` and `GetEnabledByProtocol` only queried `sabnzbd` for the usenet protocol; NZBGet was never returned, causing "no enabled download clients" on every grab attempt for users with only NZBGet configured.
- **NZBGet credentials zeroed on read** (#396) — `hydrateClientCredentials` blanked `username`/`password` for all non-qBit/Transmission clients, silently wiping NZBGet HTTP Basic auth credentials before they reached the adapter.
- **Deluge missing from torrent protocol selector** (#396) — both `GetFirstEnabledByProtocol` and `GetEnabledByProtocol` excluded Deluge from the torrent client `IN` list, causing "no enabled download clients" for Deluge-only setups.
- **Imageproxy concurrent-write race** (#396) — concurrent requests for the same image URL all wrote to the shared `imgFile+".tmp"` path; a racing `O_TRUNC` open could zero the file while another goroutine renamed it into the cache, resulting in empty image responses. Each goroutine now uses `os.CreateTemp()` for an isolated temp file.

## [v1.2.5] — 2026-04-24

### Added

- **`{Series}` and `{SeriesNumber}` naming tokens** (#389) — file renaming templates now support `{Series}` (primary series name) and `{SeriesNumber}` (position in series, e.g. `3` or `3.5`). Both are looked up at import time from the `series_books` join table; books with no series silently omit the path segment so existing templates are unaffected. The audiobook destination template exposes the same tokens. Default template is unchanged.
- **Scanner series-position matching** (#390) — the library scanner now attempts a fourth matching tier: if a filename contains a series name and position number (e.g. `[Dune Chronicles, Book 2]` or `(Mistborn #1)`) and no title/author match was found, Bindery looks up the series in the database and reconciles the book if the match is unambiguous. Supports bracket and parenthesis notation, `book/vol/part` prefixes, and integer or decimal position numbers. ISBN-shaped numbers are excluded via a letter-start requirement on the series name.

### Fixed

- **Discover: unrated and low-popularity books suppressed** (#391, closes #360) — `hardFilter` now drops candidates with fewer than 50 ratings (obscure long-tail editions that have never been rated) and candidates with 50+ ratings but an average below 3.0 (objectively poor books). Candidates with no ratings data at all are not penalised so missing metadata doesn't hide results.
- **Discover: box sets, omnibuses, and anthology contributions excluded** (#392, closes #361) — a new keyword scan in `hardFilter` drops titles matching "omnibus", "box set", "complete works", "complete collection", "anthology", "collected works", "the best of", and similar multi-volume markers. Users see individual titles on the Discover page rather than compilation volumes they may already own in parts.
- **Download client forms: Use SSL toggle and URL Base field added** (#393, closes #364) — both "Add client" and "Edit client" forms now expose a **Use SSL** checkbox and a **URL Base** text field. Previously these settings existed in the Go model and DB schema but were invisible in the UI, so operators behind a reverse-proxy subpath or needing TLS had no way to configure them without raw DB edits. `urlBase` is also added to the TypeScript `DownloadClient` interface which was missing it.
- **Download client URLs now respect `url_base`** (#375, closes #369) — all five downloader clients (qBittorrent, Transmission, Deluge, NZBGet, SABnzbd) built their connection URL from `host:port` only, ignoring the stored `url_base`. Operators running a client behind a reverse-proxy subpath (e.g. `/qbit`) would see Bindery connect to the wrong endpoint. A new `internal/downloader/urlbase.Normalize()` helper canonicalises the stored value — handles missing leading slash, trailing slashes, and pasted full URLs — and the result is threaded through every `New()` constructor.

## [v1.2.4] — 2026-04-24

### Fixed

- **Non-latin author names now match usenet releases** (#380) — authors whose names are written in a non-latin script (e.g. Japanese, Chinese, Arabic) have an ASCII surname of `""` after tokenisation, so every release was filtered out as irrelevant. Bindery now fetches the author's OpenLibrary `alternate_names` on first add, saves any ASCII-script aliases to `author_aliases`, and includes those aliases when building the surname candidate list for release matching.
- **Import no longer stalls indefinitely on NFS timeouts** (#381) — the file-copy path used bare `io.Copy` with no cancellation. On an NFS stall the goroutine blocked forever and the download record stayed locked in `importing` state. Each import now runs under a 30-minute context timeout; copies run in a goroutine and respect cancellation, closing both file descriptors to help the kernel unblock the stalled call.
- **Wanted filter no longer shows unmonitored books** (#382) — the Books page Wanted status filter matched `status === 'wanted'` without checking `monitored`, so books explicitly set to "don't monitor" appeared alongside genuinely wanted titles. The filter now requires `monitored === true` when the Wanted status is active.
- **Recommender language filter applied to candidates** (#359) — `hardFilter` removed owned, dismissed, and excluded-author candidates but never checked language. Users with a preferred language set would receive foreign-language recommendations. Candidates whose `Language` field differs from `PreferredLanguage` are now filtered out; candidates with an empty language tag pass through so missing metadata doesn't silently hide results.
- **Recommender recency score anchored to library median year** (#357) — `recencyScore` used `time.Now().Year()` as its reference point, penalising any book published before ~2005 regardless of the user's actual reading taste. The score is now relative to the median publication year of the user's library (computed in `BuildProfile`). Window widened from 20 → 30 years, floor lowered from 0.3 → 0.1, and `weightRecency` bumped from 0.10 → 0.15 to give the era-relative signal more influence.

### Changed

- **CI: parallel validate jobs and reduced friction** — the `validate` job split into `validate-go` (race-detector tests) and `validate-frontend` (npm build) running in parallel, cutting PR critical-path time from ~253 s to ~180 s. `golangci-lint` and `govulncheck` removed from the security workflow's `sast-go` job (both already run in `lint`). BuildKit GHA layer-cache added to container scans. Security workflow now skips doc-only changes via `paths-ignore`. Kubesec and Dockle removed (output was silently discarded). Discord release announcements now posted automatically on tag push.

## [v1.2.3] — 2026-04-23

### Fixed

- **Logs tab now displays entries** — `db.LogEntry` was missing `json:""` tags, causing Go to serialise field names as PascalCase (`ID`, `TS`, `Level`, `Component`, `Message`, `Fields`). The TypeScript interface expected camelCase, so every row rendered blank. (#376)

## [v1.2.2] — 2026-04-23

### Fixed

- **Calibre "Push all to Calibre" button state now matches Test-connection result** (#342) — the button was enabled even when the last connectivity test failed, allowing pushes to silently no-op against an unreachable bridge. It now stays disabled until a successful test in the current session.
- **Download client host field no longer double-schemes the URL** (#353) — if a user typed `https://` or `http://` in the Host field, the downloader prepended the scheme a second time, producing `https://https://…` and causing every connection attempt to fail. The host is now stripped of any leading scheme before the URL is assembled.
- **CSP nonce now injected server-side** (#353) — the inline `<script>` tag in `index.html` used a static placeholder nonce that never matched the server-generated nonce, causing the theme-initialisation script to be blocked by Content-Security-Policy in strict environments. The nonce is now written by the Go server at request time.
- **Docker image tags now include `v`-prefixed semver variants** (#353) — the CI metadata action was missing `type=semver,pattern=v{{version}}`, so only bare `1.2.x` tags were pushed to ghcr.io. Both `1.2.x` and `v1.2.x` are now available.
- **Version string in footer links to the GitHub releases page** (#356) — clicking the version badge now opens the corresponding release regardless of whether the string is a semver, `v`-prefixed semver, or `dev-<sha>`.

## [v1.2.1] — 2026-04-23

### Fixed

- **Prowlarr-synced indexers no longer send broad parent category 7000** (#344) — indexers synced from Prowlarr were always requesting category 7000 (Books parent), which caused many indexers to return results for every book-adjacent category including comics. Bindery now sends the appropriate child category (7020 Ebooks, 3030 Audiobooks) and drops the parent when children are present.
- **qBittorrent "hash could not be determined" on category mismatch** (#363) — after adding a torrent, Bindery polled only the configured download category, so if qBittorrent placed the torrent in a different category the hash was never found and the download was marked as failed. Bindery now polls the full torrent list (unfiltered) and logs a detailed error with hash diagnostics if the 30-second window expires.
- **Dual-format delete leaves orphan sibling files** (#343) — deleting one format of a dual-format book failed to remove sibling format files from disk. Sibling cleanup now runs regardless of whether the file being deleted still exists.
- **Rescan misbinds books with similar titles** (#290) — the Jaro-Winkler similarity threshold for matching filenames to book records was too permissive. Threshold raised from 0.80 to 0.88.
- **Interactive search mixes ebook and audiobook results** (#333) — results from all indexers were shown in a single unsorted list for dual-format books. Results are now split into labelled **Ebook results** and **Audiobook results** sections.

## [v1.2.0] — 2026-04-22

### Added

- **Default library location can now be set from Settings → General** (#332). A new "Default root folder" dropdown lets you pick any configured root folder as the library path used when an author has no per-author root folder. Existing `BINDERY_LIBRARY_DIR` continues to work as a fallback when the setting is unset. An inline "Add root folder" affordance lets you create a new root folder without leaving the page. Startup logs a warning (but does not fail) if the configured default root folder no longer exists on disk.
- **Search results grouped by media type** — For dual-format books (ebook + audiobook), the Book Detail page now displays results in two titled sections (Ebooks / Audiobooks) each with its own 20-result cap, so audiobook results can no longer fall past the UI cap. Single-format books retain the existing flat list. Each result row in the split view shows a colour-coded media-type badge (#333).
- **Persistent log store** — Settings → Logs now persists entries across restarts and supports filtering by date range, level, and component. Retention defaults to 14 days and is configurable via `BINDERY_LOG_RETENTION_DAYS` or Settings → General → Log retention. ([#241](https://github.com/vavallee/bindery/issues/241))

### Fixed

- **Multi-file ebook downloads are now fully tracked** — Delete + files removes every file (mobi, epub, pdf, etc.) and rescan cannot re-claim orphan files. Library rescan now requires a matched file to live under the candidate book's configured root folder, preventing cross-author mismapping (#343).
- **Ebook searches no longer include the parent Books category (7000)**, which could return comics and magazines. Affects Prowlarr-synced indexers: `filterCategoriesForMedia` now matches only the 702x ebook subcategory range (7020–7029) and 303x audiobook range (3030–3039), and the syncer drops parent categories (7000, 3000) at sync time and propagates category changes on re-sync. (#344)
- **Author sync no longer creates duplicate book rows that differ only in edition suffix, whitespace, or Unicode normalization.** Existing duplicates are merged on upgrade. Search result filtering no longer drops valid releases when the book title contains a parenthesised edition qualifier (#283).

## [v1.1.7] — 2026-04-22

### Fixed

- **Discover page blank after first refresh** — `models.Recommendation.Genres` was typed as `string`, so the API serialised genres as a JSON-encoded string (`"[\"Fantasy\",...]"`) instead of a JSON array. The frontend called `.map()` on the string, threw a `TypeError`, and React unmounted the whole page. `Genres` is now `[]string`; the DB scan layer deserialises the stored JSON before the struct is marshalled to the API response.

## [v1.1.6] — 2026-04-22

### Fixed

- **Discover page always empty** — `BuildProfile` added every book to `OwnedForeignIDs` regardless of status, so all candidate generators (series, author_new, genre_similar, serendipity) immediately skipped every known book and returned zero results. `OwnedForeignIDs` now only includes books with `downloaded` or `imported` status, allowing `wanted`/`skipped` books to surface as recommendations.

## [v1.1.5] — 2026-04-22

### Fixed

- **Authors tab empty after adding author** (#339) — authors added via the UI or the AddBook implicit-create path were stored with `owner_user_id = NULL` because `Create()` always called `CreateForUser` with a hardcoded zero. The Authors list query filters by the authenticated user's ID, so every freshly-added author was invisible in the tab even though it existed in the database. Both creation sites now pass the user ID from the request context, so authors are correctly owned and appear immediately.

## [v1.1.4] — 2026-04-21

### Fixed

- **Calibre import: Language unknown on all books** (#314) — `books_languages_link` was never queried, so every imported book showed "Language unknown" and editions were hardcoded to `"eng"`. The reader now reads the primary ISO 639-2 language code from Calibre's languages table (falls back gracefully for pre-0.7 Calibre libraries that predate the table). Language is applied to both the book row and the edition.
- **Calibre import: Author ratings missing after first Refresh Metadata** (#314) — `relinkCalibreAuthor` fetched the full OL author but only copied image/description/sort name. `ratings_count` and `average_rating` are now copied as well.
- **Calibre import: Duplicate book rows after Refresh Metadata** (#314) — `FetchAuthorBooks` skipped title-matched books with a bare `continue`, leaving calibre-imported stubs (synthetic `calibre:book:N` ForeignID, no language) un-upgraded when OpenLibrary returned the same title. Calibre stubs are now updated in-place with the real OL ForeignID and language instead of being silently skipped, preventing a second OL row from being created alongside them.

## [v1.1.3] — 2026-04-21

### Fixed

- **Authors missing from list view** (#330) — authors with a NULL `owner_user_id` (created before the multi-user migration backfill ran, or imported without a user context) were silently excluded from `GET /api/v1/author`. The list query now includes `OR owner_user_id IS NULL` so all owned authors appear regardless of when they were added.
- **Delete file leaves zombie on disk** (#290) — `DELETE /book/{id}/file` on legacy books (only `file_path` set, no `ebook_file_path`) cleared the DB column but never called `os.Remove` on the actual file. The legacy path is now handled explicitly in the deletion block.
- **Download link missing for newer books** (#331) — `GET /api/v1/book/{id}/file` returned 404 for books added after the dual-format schema (migration 026+) because it only checked the legacy `file_path` column. It now falls back to `ebook_file_path` then `audiobook_file_path`. The book detail page also hid the "Download file" button for these books; it now appears whenever either path is present.

## [v1.1.2] — 2026-04-21

### Changed

- Prowlarr package test coverage expanded from ~0% to ~98% — adds `client_test.go` covering all HTTP paths and `syncer_extra_test.go` with error-path and edge-case tests for the syncer.
- Pinned all GitHub Actions in `scorecard.yml` to commit SHAs (OpenSSF Scorecard `Pinned-Dependencies` compliance).

## [v1.1.1] — 2026-04-21

### Security

- **API key exposed to non-admin users** — `GET /api/v1/auth/config` returned the global API key to every authenticated account. Since the key is also accepted via the `?apikey=` query string, any regular user could authenticate with full API access. The key is now redacted unless the caller has `role=admin`.
- **Cross-user author visibility** — `GET /api/v1/author` returned all authors regardless of `owner_user_id`, letting one user see (and enumerate) another user's library. The list is now scoped to the authenticated user.
- **Non-admin auth-mode escalation** — `PUT /api/v1/auth/mode` lacked a `RequireAdmin` guard. A regular user could switch the instance to `local-only`, granting unauthenticated access to every client on the local network. The endpoint now requires admin role.
- **Untrusted `X-Forwarded-*` header injection** — `X-Forwarded-Proto` and `X-Forwarded-Host` were accepted from any client when `BINDERY_TRUSTED_PROXY` was not set, enabling OPDS base-URL injection and spurious HSTS headers. All forwarded headers are now stripped from requests that do not originate from a configured trusted proxy.

## [v1.1.0] — 2026-04-20

### Added

- **Audible-direct author lookup** (#302) — audiobook-heavy users no longer lose most of a prolific author's Audible catalogue to OpenLibrary/Hardcover ASIN gaps. When the effective media type is `audiobook` or `both`, `FetchAuthorBooks` supplements OpenLibrary's works list with results from Audible's public catalogue endpoint (`api.audible.com/1.0/catalog/products`). Supplemental books flow through the same dedup + metadata-profile `allowed_languages` filter as OpenLibrary, so foreign-language ASINs are filtered out before persisting.

### Fixed

- **Author ingestion drops books + catalogue noise** (#313) — `GetAuthorWorks` now uses OpenLibrary's search index as the primary source (one call returns title + language + subjects + cover + year) and keeps the `/authors/{id}/works` endpoint as a backfill that hydrates series membership and picks up works the search index has missed (e.g. recent releases). A new subject/title noise filter at the OL client layer drops study guides, summaries, film/TV adaptations, screenplays, and audio-CD pseudo-works before they reach the ingestion pipeline, stopping duplicates like the five "Dutch House" entries previously pulled for Ann Patchett.
- **Audiobook library scan misses tagged files** (#303) — the library scan now reads embedded ID3/iTunes tags (title, author, ASIN) from MP3/M4B/M4A/FLAC/OGG files during reconciliation. Match priority is ASIN → tag title+author → fuzzy filename fallback, so well-tagged Audible/organised libraries match without manual correction. Files whose tags can't be read surface as scan warnings and a new `tag_read_failed` counter in `library.lastScan`.

### Chores

- **golangci-lint cleanup** — resolve errorlint (`%v` → `%w` for double-wrapped errors), staticcheck (apply De Morgan on ASIN charset check), and gofmt formatting noise introduced alongside the three fixes above.

## [v1.0.5] — 2026-04-20

### Fixed

- **Admin role required on fresh install** (#321) — new users created via first-run setup were stored without the admin role, so Settings → Config showed only the General section and any config mutation (Calibre plugin, users, indexers) 403'd with "admin role required" regardless of the security mode. First-run user is now explicitly promoted to admin before the session is issued; existing single-user installs are unaffected. Unblocks #314 reporters from retesting the Calibre metadata fix.
- **NZB grabs misrouted to qBittorrent** (#320) — Prowlarr-synced indexers were hardcoded as `torznab` regardless of the upstream indexer's actual protocol, so NZB search results were tagged `protocol=torrent` and dispatched to qBittorrent, which then failed with `add torrent accepted but hash could not be determined`. The syncer now uses Prowlarr's `protocol` field to choose `newznab` vs `torznab`, and corrects mis-typed rows on the next sync. The scheduler no longer falls back across protocols when the protocol-matched client list is empty — an NZB release can never be pushed to a torrent client.

### Added

- **Bulk media-type update across monitored authors** (#247) — select multiple authors on the Authors page and switch their media type in one action (or flip all authors from a Settings one-shot). `PATCH /api/v1/authors/bulk` re-evaluates wanted/missing status for affected books so a flip from ebook → audiobook (or reverse) doesn't leave the catalogue in an inconsistent state. Companion to the existing global default media-type setting.

### Docs

- **Discord invite added to README and CONTRIBUTING** (#319) — new Community section links the BINDERY Discord server as a real-time channel for support and contributor onboarding, alongside GitHub Issues and Discussions. Security reports continue to go through `SECURITY.md`, not Discord.

### Chores

- **vitest 3.2.4 → 4.1.4** (#312) — dev-dependency bump for the web test runner.

## [v1.0.4] — 2026-04-20

### Reverted

- **eslint 9→10 bump** (#311) — reverted because `eslint-plugin-react-hooks@7.0.1` still peers eslint at `^9.0.0`, breaking `npm ci` in the Docker build. v1.0.3 tag was cut but never produced an image; v1.0.4 ships the same fixes without the eslint upgrade. Will retry once react-hooks catches up.

## [v1.0.3] — 2026-04-20 *(tagged but not released — CI build failure, see v1.0.4)*

### Fixed

- **CSRF token lost on page reload** (#315) — sessions survived reloads cookie-wise, but `initCSRF()` only ran on login, so subsequent mutations hit 403 until next login. `AuthContext.refresh()` now re-hydrates the token whenever the session is authenticated.
- **Calibre-imported authors stuck with no metadata** (#316) — authors with a `calibre:` foreign ID were hard-skipped by the metadata refresh. They are now re-linked to the metadata provider on first refresh via exact name match (case/whitespace-insensitive), pulling real image/description/sort name in place.
- **Misleading author search book count** (#317) — the "books" number on author search results is OpenLibrary's raw work count before dedup/language-filter; relabelled to "up to N works" with a tooltip explaining the post-filter catalogue will be smaller.

### Docs

- **Auth, multi-user, and v1 upgrade guides** (#318) — added `docs/auth-multiuser.md`, `docs/auth-oidc.md`, `docs/auth-proxy.md`, `docs/multi-user.md`, `docs/troubleshooting-auth.md`, `docs/upgrade-v2.md` covering the v1.0 role model, OIDC/proxy setup, and migration path.

### Chores

- Dependency bumps: golang base image (#307), node base image (#308), `modernc.org/sqlite` (#309).

## [v1.0.2] — 2026-04-20

### Added

- **Admin password reset** (#292/#305) — admins can reset any local user's password from the Users page without requiring the user to log out. New endpoint `PUT /auth/users/{id}/reset-password`.

### Fixed

- **Books sort with missing release dates** (#304) — books without a release date were bubbling to the top of "oldest first" date sorts. They now sort to the end in both directions.
- **Empty folders left behind after deleting books** (#290/#306) — when the last file in a book folder was removed, the now-empty parent directory stayed behind. `removeBookPath` now cleans up the parent on a best-effort basis. Multi-format folders shared between ebook + audiobook are only removed when both formats are gone.

## [v1.0.1] — 2026-04-19

### Fixed

- **Admin UI missing from v1.0.0** — the Users management page (`/users`), admin nav icon, role-gated Settings tabs, and `isAdmin` context were omitted from the v1.0.0 build. All admin UI components now ship correctly (#301).

## [v1.0.0] — 2026-04-19

### Added

- **Reverse-proxy SSO** (#238/#239) — new `proxy` auth mode trusts an upstream identity header (`X-Forwarded-User` by default) when the request arrives from a configured trusted proxy IP. Startup refuses proxy mode without `BINDERY_TRUSTED_PROXY` set.
- **Native OIDC client** (#237) — Authorization Code + PKCE with multi-provider support (Google, GitHub/Dex, Authelia, Keycloak). Providers configured via Settings → Authentication; users identified by stable `(issuer, sub)` pair.
- **Multi-user scoping** (#236) — every user-owned entity (authors, books, downloads, profiles, root folders) scoped to its owner. First user auto-promoted to admin; admin users manage all settings.
- **Admin user management** — Users page (admin only): invite users, set roles, delete with last-admin guard.
- **Settings split** — per-user tabs (API key, password, notifications) and admin-only tabs (indexers, clients, profiles, system).
- **CSRF double-submit tokens** (#240) — `GET /auth/csrf` issues a session-bound token; all authenticated mutations require matching `X-CSRF-Token` header. API-key requests exempt.

### Fixed

- **CSRF middleware login bypass** — `POST /auth/login` was incorrectly blocked by CSRF check before a session cookie existed; fixed to skip CSRF when no session is present.

### Breaking

- **Database migration 025**: `owner_user_id` added to all user-owned tables; existing data migrated to user ID 1. Back up before upgrading. See [upgrade guide](https://github.com/vavallee/bindery/wiki/Howto-Migrate-to-multi-user).
- **CSRF tokens required**: browser-based API consumers must call `GET /auth/csrf` and include `X-CSRF-Token` on mutations. API-key clients unaffected.

## [v0.22.0] — 2026-04-19

### Fixed

- **Grab FK constraint crash** — clicking Grab on a search result no longer fails with a foreign-key violation; `bookId`/`indexerId` are now treated as optional nullable fields so free-text search grabs always succeed (#285)
- **Audiobook search details blank page** — `SearchDebug.Filters` is now initialised to an empty slice instead of `null`, preventing a JS crash in `SearchDebugPanel` that was more likely to trigger on audiobook searches (#282)
- **ISBN lookup cryptic error** — unknown ISBNs now surface a friendly "No book found for ISBN X. Check the number, or try searching by title instead." message instead of a misleading upstream-unavailable error (#284)

### Improved

- **Calibre Test Connection feedback** — the Test Connection button now shows ✓/✗ prefixes and the exact plugin reachability message; stale results are cleared whenever Calibre settings are saved (#262)

## [v0.21.0] — 2026-04-19

### Added

- **Spanish, Filipino, and Indonesian UI translations** — language switcher now offers English, Français, Deutsch, Español, Filipino, Nederlands, and Bahasa Indonesia. Browser language is auto-detected on first visit; manual override persists to localStorage.
- **Search hourglass icon** — the Search nav item moves off the main navigation bar and becomes an hourglass icon next to the settings gear, keeping the header cleaner. On mobile it remains accessible as a text item in the hamburger dropdown.

## [v0.20.3] — 2026-04-19

### Security

- **Trusted proxy configuration** — `BINDERY_TRUSTED_PROXY` gates `X-Forwarded-For` rewriting to a configured proxy IP/CIDR. Without it, forwarded headers are ignored and the direct peer IP is used, preventing XFF spoofing in local-only auth mode (mirrors Sonarr CVE-2026-30975).
- **File download path validation** — the file download endpoint now verifies `book.FilePath` falls within a configured library root before serving. Paths outside `BINDERY_LIBRARY_DIR` / `BINDERY_AUDIOBOOK_DIR` return 403.
- **CSRF header exemption for API key requests** — the `X-Requested-With` CSRF check now correctly exempts API-key-authenticated requests; only cookie-session requests are required to supply the header.
- All fixes from v0.20.1: SSRF validation on Prowlarr URLs, path traversal protection in file renamer, strict backup filename regex, image proxy redirect re-validation, Hardcover token moved to Authorization header, OPDS rate limiting, CI hardening.

## [v0.20.0] — 2026-04-18

### Added

- **Deluge download client** ([#263](https://github.com/vavallee/bindery/pull/263)) — adds Deluge alongside qBittorrent and Transmission as a supported torrent client. Configure under Settings → Download Clients with host, port (default 8112), password, and optional label (requires the Label plugin). Deluge authenticates with a single password and no username, which the UI reflects.
- **Direct indexer search page** ([#266](https://github.com/vavallee/bindery/pull/266)) — a new **Search** nav item runs freeform queries across every configured indexer without needing a tracked book. Each result row has a **Grab** button that sends the release straight to the download client, bypassing the per-book decision pipeline. Useful for grabbing one-off titles or testing indexer responses.
- **{ASIN} naming token** ([#269](https://github.com/vavallee/bindery/pull/269)) — `{ASIN}` can now be used in rename templates (e.g. `{Author}/{ASIN}/{Title}.{ext}`). ASINs are also extracted from filenames during library scans and stripped from the title, so Amazon-origin files no longer pollute title matching. Empty string when the book has no ASIN in its metadata.

### Fixed

- **Indexer Test probes with a real search** ([#265](https://github.com/vavallee/bindery/pull/265)) — after the caps probe, **Test** now runs a `t=search&q=book` request across the indexer's book categories. The UI surfaces an amber warning when zero results are returned, catching misconfigured API keys and category mappings that previously reported success on caps alone.

## [v0.19.2] — 2026-04-18

### Fixed

- **Create destination directory before audiobook import** ([#255](https://github.com/vavallee/bindery/pull/255)) — import no longer fails when the target library directory does not yet exist; Bindery now creates it before attempting to move files. Resolves a silent failure that left audiobooks stranded in the download folder.
- **Search consistency for "both" media type** ([#256](https://github.com/vavallee/bindery/pull/256)) — books monitored as `both` now run separate ebook (7xxx) and audiobook (3xxx) category searches and union the results, instead of falling through to the ebook branch only. Also normalises subtitle-heavy query strings to improve match rates on all indexers.

### Added

- **Unknown-language badge and pass/fail setting** ([#257](https://github.com/vavallee/bindery/pull/257)) — books whose language metadata is absent or unrecognised are now surfaced with an "unknown language" badge in the UI. A new setting controls whether unknown-language books pass or fail the language filter, giving users explicit control instead of silent rejection.
- **Search debug panel** ([#258](https://github.com/vavallee/bindery/pull/258)) — a collapsible debug panel on the Book Search page shows every indexer that was queried, how many results each returned, and which pipeline stage (dedupe, junk filter, relevance, language, decision) dropped each candidate. The last debug payload is cached server-side so the panel survives page reloads.
- **Push all to Calibre sync button** ([#259](https://github.com/vavallee/bindery/pull/259)) — a single button on the Calibre settings page triggers a full library sync, pushing every imported book to Calibre in one shot. Useful after initial setup or after a Calibre database restore.
- **Global default media type and bulk author update** ([#260](https://github.com/vavallee/bindery/pull/260)) — a new global setting establishes the default media type for newly added authors. A bulk-update action on the Authors page lets you apply any media-type change across all (or selected) authors at once, eliminating tedious one-by-one edits.

## [v0.19.1] — 2026-04-18

Re-release of the v0.19.0 feature set plus the external-import and newznab-coverage PRs. The previously-tagged `v0.19.0` artifact predated these merges; `v0.19.1` is the authoritative release for this feature batch.

### Added

- **NZBGet download client** ([#233](https://github.com/vavallee/bindery/pull/233)) — adds NZBGet alongside SABnzbd as a Usenet download target. Configure under Settings → Download Clients; Bindery tracks grabs, monitors status, and imports completed downloads the same way it does for SABnzbd.
- **External import mode for Calibre / Grimmory workflows** ([#235](https://github.com/vavallee/bindery/pull/235)) — new import mode that lets Bindery hand completed downloads off to an external importer (Calibre `calibredb add` in a sidecar, or Grimmory's ingest pipeline) instead of moving files directly into the library. Useful when another tool owns the final file layout.
- **Series gap detection and Fill gaps** ([#234](https://github.com/vavallee/bindery/pull/234)) — the Series page now shows how many books are missing from each series ("N missing" badge) and a **Fill gaps** button that marks all non-imported entries as Wanted and kicks off indexer searches immediately. No more manually hunting for which entries you're missing.
- **Series monitoring toggle** — mark a series as monitored so it's easy to identify which series you're actively tracking. Foundation for future automation (auto-adding new entries when they appear).
- **Indexer Test button reports HTTP status, categories, and latency** ([#243](https://github.com/vavallee/bindery/pull/243)) — clicking Test on an indexer now returns a structured probe result (status code, category count, `bookSearch` availability, round-trip latency) instead of a bare "OK / failed" string. Makes misconfigured endpoints and slow indexers much easier to diagnose.
- **Import failure reason surfaced in Queue and History** ([#244](https://github.com/vavallee/bindery/pull/244)) — failed imports now record and display the underlying error (permission denied, path missing, Calibre rejected the file, etc.) instead of silently disappearing. Addresses the top recurring pain point from user feedback: "silent failures make it impossible to know what went wrong."
- **Storage paths visible in Settings UI** ([#245](https://github.com/vavallee/bindery/pull/245)) — download, incoming, and library paths are now surfaced in Settings → Storage so users can confirm where Bindery is reading from and writing to without digging through env vars or ConfigMaps.
- **Auto-grab checkbox persists in Add Author dialog** ([#242](https://github.com/vavallee/bindery/pull/242)) — the "auto-grab on add" toggle now remembers its last value across dialog opens, so users who always (or never) want auto-grab don't have to reset it every time.

### Fixed

- **Indexers tab crash** ([#227](https://github.com/vavallee/bindery/pull/227)) — clicking Settings → Indexers caused a white screen for users without Prowlarr configured.
- **Language filter now rejects books with unknown language** ([#228](https://github.com/vavallee/bindery/pull/228)) — non-English editions no longer slip through English-only metadata profiles when OpenLibrary omits language data.

### Internal

- **Newznab indexer client test coverage** ([#251](https://github.com/vavallee/bindery/pull/251)) — lifted `internal/indexer/newznab` coverage from 56.6% to 89.5% with focused tests for BookSearch tier fallbacks, Probe result shape, URL normalization, and error paths.

## [v0.19.0] — 2026-04-18

Initial tag for the above feature batch; artifact was published before the PRs it was meant to include were merged. Use `v0.19.1` or newer — this tag is retained only for historical reference.

## [v0.18.3] — 2026-04-17

### Fixed

- **Language filter now rejects books with unknown language** ([#228](https://github.com/vavallee/bindery/pull/228)) — when a metadata profile restricts to specific languages (e.g. English-only), books with no language data are now rejected instead of passing through. OpenLibrary doesn't include language at the work level, so translated editions (Turkish, Spanish, Dutch, etc.) were silently ingested for English-only profiles. The OL client already does a best-effort search-index lookup; this closes the gap for works the index doesn't cover. Reported in [#224](https://github.com/vavallee/bindery/issues/224).

## [v0.18.2] — 2026-04-17

### Fixed

- **Indexers tab crash** ([#227](https://github.com/vavallee/bindery/pull/227)) — clicking Settings → Indexers caused a white screen for users without a Prowlarr instance configured. The `/api/v1/prowlarr` endpoint returned JSON `null` (Go nil slice) instead of `[]`, which crashed React on render. Reported in [#225](https://github.com/vavallee/bindery/issues/225).

## [v0.18.1] — 2026-04-17

### Changed

- **Plugin API key field UX** ([#221](https://github.com/vavallee/bindery/pull/221)) — the API key field in Settings → Calibre (plugin mode) now has a show/hide toggle (eye icon) and a **Generate** button that fills the field with a cryptographically random 32-byte hex key (`crypto.getRandomValues`). `autoComplete="off"` prevents browsers from injecting saved passwords. Paste is unrestricted.

## [v0.18.0] — 2026-04-17

Calibre plugin integration mode, decision engine, pending releases, and state machine for downloads.

### Added

- **Calibre plugin integration mode** ([#212](https://github.com/vavallee/bindery/pull/212)) — new `plugin` mode alongside `calibredb`. When selected, Bindery POSTs imported file paths to the Bindery Bridge Calibre plugin over HTTP instead of shelling out to `calibredb`. Allows Calibre to run in a separate pod/container from Bindery without requiring a shared binary or library volume. Configure via Settings → Calibre → Mode: Plugin, with Plugin URL and API Key fields.
- **Release decision engine** ([#218](https://github.com/vavallee/bindery/pull/218)) — specification-pattern engine evaluates candidate releases against per-profile rules before grabbing, replacing ad-hoc inline checks.
- **Pending releases table** ([#219](https://github.com/vavallee/bindery/pull/219)) — delay-held results are tracked in a dedicated pending table with UI, replacing the previous silent-drop behaviour.
- **Prowlarr native indexer sync** ([#215](https://github.com/vavallee/bindery/pull/215)) — Bindery can now sync indexer configurations directly from a Prowlarr instance.
- **Add book by title or ISBN** ([#216](https://github.com/vavallee/bindery/pull/216)) — search bar accepts ISBNs and free-text titles in addition to author queries.
- **qBittorrent hash retry** ([#209](https://github.com/vavallee/bindery/pull/209)) — retries hash lookup for 10 s after torrent URL add, fixing race where hash was not yet visible after `.torrent` fetch.

### Changed

- **Download state machine** ([#217](https://github.com/vavallee/bindery/pull/217)) — formalises download lifecycle states; replaces ad-hoc string constants with typed enum.
- **Calibre settings tab** ([#220](https://github.com/vavallee/bindery/pull/220)) — shared `library_path` field hoisted to top of Calibre tab for clarity.

### Upgrade notes

- **No breaking schema migrations** — additive only. Safe drop-in replacement.
- **Calibre plugin mode** requires the [Bindery Bridge Calibre plugin](https://github.com/vavallee/bindery-plugins) installed in your Calibre instance.

## [v0.17.0] — 2026-04-17

Drop-folder Calibre mode removed, OpenLibrary series schema fixed, and a batch of UX and deployment polish.

### Removed

- **Calibre drop-folder mode** ([#207](https://github.com/vavallee/bindery/pull/207)) — the drop-folder integration has been removed entirely. It depended on the Calibre GUI application's auto-add watcher, which never fires in containerised / headless deployments. Books silently timed out with no feedback. The `calibredb` mode achieves the same result — mirroring every successful import into Calibre — without any of these constraints: it only requires that Bindery and Calibre share the library directory via a volume mount, which is already required for the library import/sync feature. Existing `calibre.mode = drop_folder` settings are treated as `off`; operators should switch to `calibredb` mode. The `calibre.drop_folder_path` setting and the `/api/v1/calibre/test-paths` endpoint are gone.

### Fixed

- **OpenLibrary object-typed series entries** ([#206](https://github.com/vavallee/bindery/pull/206), closes [#201](https://github.com/vavallee/bindery/issues/201)) — some OpenLibrary work records encode `series` as `[{"key": "...", "title": "..."}]` (object array) rather than `["..."]` (string array). Bindery previously crashed with an unmarshal error on these records, silently skipping all books for authors like Pierce Brown, J.K. Rowling, and Cornelia Funke. A new `flexStringSlice` decoder accepts both forms transparently.
- **Calibre settings save errors** ([#202](https://github.com/vavallee/bindery/pull/202), closes [#175](https://github.com/vavallee/bindery/issues/175)) — validation errors on `PUT /api/v1/setting/calibre.*` were returned as 400 but the UI silently discarded the response body; the error message now surfaces in the Settings page. Also fixes a case-sensitivity bug where NFS paths with uppercase letters were rejected.
- **Search "no indexers" message** ([#203](https://github.com/vavallee/bindery/pull/203)) — when a search returns no results *and* no indexers are configured, the UI now shows "No indexers configured — add one in Settings" instead of the generic "No results" empty state.
- **Login form method** ([#195](https://github.com/vavallee/bindery/pull/195)) — login form missing `method="POST"` caused browsers to silently submit via GET, leaking credentials in the URL bar and query-string logs.
- **Auth visibility refresh** ([#199](https://github.com/vavallee/bindery/pull/199)) — auth status was not rechecked when a browser tab regained focus after a session expiry, leaving users on a page that appeared authenticated but returned 401 on the next action.
- **Books empty state** ([#197](https://github.com/vavallee/bindery/pull/197)) — Books page showed a bare spinner when the library was empty; now shows instructional copy pointing to the "Add author" flow.
- **Version badge and footer links** ([#196](https://github.com/vavallee/bindery/pull/196)) — version badge in the header now links to the corresponding GitHub release; footer links to the repo.
- **Calendar aria-labels** ([#198](https://github.com/vavallee/bindery/pull/198)) — previous/next month buttons on the calendar lacked `aria-label` attributes, failing screen-reader and accessibility audits.

### Changed

- **Per-page document titles** ([#200](https://github.com/vavallee/bindery/pull/200)) — each page sets `document.title` to reflect the current view (e.g. "Authors — Bindery", "Settings — Bindery") for browser tab identification and history navigation.
- **Helm chart: corrected `BINDERY_DOWNLOAD_PATH_REMAP`** ([#204](https://github.com/vavallee/bindery/pull/204)) — default remap was `/downloads:/downloads`; corrected to `/downloads:/media` to match the NFS-mount convention documented in the reference deployment.
- **ArgoCD reference application** ([#205](https://github.com/vavallee/bindery/pull/205)) — updated NFS volume configuration and container entrypoints in the reference ArgoCD application manifest.

### Upgrade notes

- **No schema migrations** — this is a pure-logic and UI release. Drop-in binary or image replacement is safe.
- **Drop-folder users:** if `calibre.mode` is set to `drop_folder`, Bindery will treat it as `off` on startup. Switch to `calibredb` mode in Settings → Calibre to restore automatic mirroring. The `calibre.library_path` and `calibre.binary_path` settings are unchanged.

## [v0.16.0] — 2026-04-17

Calibre library import, Settings reorganisation, stalled download detection, and image proxy hardening.

### Added

- **Calibre library import** ([#63](https://github.com/vavallee/bindery/issues/63)) — import books, authors, and editions from an existing Calibre `metadata.db` via Settings → Calibre → Library import. Incremental and idempotent; progress bar with live stats.
- **Calibre Settings tab** — Calibre settings extracted from the General tab into a dedicated tab. Eliminates the duplicate `library_path` field; single shared path at the top used by both write integration and library import. Adds "Test paths" button for drop-folder mode (validates `metadata.db` readable, drop folder writable).
- **Stalled download detection** ([#142](https://github.com/vavallee/bindery/issues/142)) — background job detects qBittorrent `stalledDL` torrents and Transmission stopped-with-error states. Automatically marks failed, blocklists the release, and triggers a re-search.

### Fixed

- **calibredb error messages** ([#160](https://github.com/vavallee/bindery/issues/160)) — "no such file or directory" now includes the path and explains the binary must be accessible inside the Bindery container, not just on the Docker host.
- **Image proxy cache** ([#138](https://github.com/vavallee/bindery/issues/138)) — sharded cache directories, LRU eviction, and atomic writes prevent corruption on large libraries.
- **Calibre import when write mode is off** — library import no longer incorrectly gated behind the write-mode toggle.
- **Calibre crash recovery** — pod restarts between `Create` and `SetCalibreID` no longer produce duplicate book rows.
- **Author sync duplicate constraint** — UNIQUE constraint on `books.foreign_id` during concurrent author syncs treated as benign skip.
- **TOCTOU-safe file copy** — importer uses `os.Root` for directory copy/hardlink to prevent symlink traversal.

### Upgrade notes

- **Schema:** migration `018_calibre_sync.sql` adds tables for Calibre library import. Drop-in binary or image replacement is safe.

## [v0.8.0] — 2026-04-14

Major feature release. Calibre users can finally automate the last mile — finished Bindery imports land in Calibre with no manual "Add books" step. Library curation gets a sharper tool: the author list stops fragmenting into "RR Haywood" / "R.R. Haywood" / "R R Haywood" duplicates, and the new **Merge authors** flow reunites them under one canonical row. Backend test coverage continues its climb, with `internal/api` and `internal/importer` both breaking 60%.

### Added

- **Calibre library integration via `calibredb`** ([#32](https://github.com/vavallee/bindery/issues/32)) — after a successful import, Bindery mirrors the book into a configured Calibre library by shelling out to `calibredb add --with-library <path>` and stores the returned Calibre book id on the Bindery book row for future OPDS and cross-library lookups. Opt-in under Settings → General → Calibre with three fields (enabled / library path / binary path) and a **Test connection** button that probes `calibredb --version`. Failures during the Calibre call are logged and swallowed so a missing binary or unreachable library never rolls back an otherwise-good Bindery import. Matches the Path A approach on the roadmap — the looser-coupled drop-folder / OPDS paths remain planned.
- **Author aliases — merge duplicate authors** ([#45](https://github.com/vavallee/bindery/issues/45)) — new `author_aliases` table plus a **Merge authors** modal on the Authors page (and a Merge button on each author's detail page). Picking a source and target reparents the source's books onto the target, deletes the source row, and preserves the source's name + OpenLibrary id as aliases pointing at the target. The add-author flow now consults the alias table: if the requested name already resolves to an existing author, the POST returns `409 Conflict` with `canonicalAuthorId` so the UI can prompt for merge instead of silently ingesting a duplicate. Two new endpoints: `GET /api/v1/author/{id}/aliases` and `POST /api/v1/author/{id}/merge`. The merge is transactional — if any child update fails, nothing changes.

### Changed

- **Backend test coverage raised to 60%+ on the two laggards** — `internal/api` now 62.7% (was 40.5%), `internal/importer` 62.2% (was 40.7%). New `_test.go` files cover the settings / custom-formats / delay-profiles / files / import-lists / migrate / notifications / quality-profiles / search / series handlers and the importer's `titleMatch` / tokenize / path-remap helpers.

### Upgrade notes

- **Schema:** two additive migrations land (`008_calibre.sql` adds a `calibre_id INTEGER` column on `books` plus three `calibre.*` settings rows; `009_author_aliases.sql` adds the `author_aliases` table). Drop-in binary or image replacement is safe.
- **Calibre is off by default.** Existing installs are unaffected until you flip the toggle in Settings → General → Calibre. The `calibredb` binary must be reachable from the Bindery process — in Docker this means either bind-mounting a calibre install or picking an image that ships `calibredb` on the PATH. A future release may publish a `bindery-calibre` image variant; track progress on [#32](https://github.com/vavallee/bindery/issues/32).
- **No duplicate-author migration is run automatically.** Existing fragmented author rows stay as-is until you merge them manually via the new modal — this is intentional, since "are these two rows the same person?" needs a human in the loop.

## [v0.7.2] — 2026-04-14

Quality release. Bulk actions land for users curating large libraries (the painful-after-CSV-import flow), the silent library-scan bug is fixed, and backend coverage jumps from 34% to 53% to quiet codecov and harden the regression safety net.

### Added

- **Multi-select / bulk actions on Authors, Books, and Wanted** ([#12](https://github.com/vavallee/bindery/issues/12)) — row checkboxes with "Select all on this page" in table headers (and overlay checkboxes on grid cards), plus a sticky `BulkActionBar` footer that appears whenever any items are selected. Authors support Monitor / Unmonitor / Search / Delete; Books additionally support Set Ebook / Set Audiobook; Wanted supports Search / Unmonitor / Blocklist (marks book as skipped and unmonitored). Three new endpoints: `POST /api/v1/author/bulk`, `POST /api/v1/book/bulk`, `POST /api/v1/wanted/bulk`. All return a per-ID result map at HTTP 200 so partial failures (e.g. a stale ID) report inline without aborting the rest of the batch.

### Changed

- **Backend test coverage raised to ≥50% (52.8% total)** — new `_test.go` files added for `internal/db`, `internal/downloader/qbittorrent`, `internal/metadata` (aggregator), `internal/metadata/googlebooks`, `internal/metadata/hardcover`, `internal/metadata/openlibrary`, `internal/notifier`, and `internal/scheduler`. No production code was modified.

### Fixed

- **Manual library scan silently aborted** ([#55](https://github.com/vavallee/bindery/issues/55)) — `POST /api/v1/library/scan` spawned the scan goroutine with the HTTP request context, which Go cancels the moment the 202 response is sent; the scan now uses `context.WithoutCancel` so it always runs to completion.

## [v0.7.1] — 2026-04-14

Build-pipeline patch. No code changes — re-cuts the v0.7.0 binary archives against a fixed GoReleaser config so the Windows / macOS / Linux downloads actually contain the frontend.

### Fixed

- **Standalone binary UI served only `.gitkeep`** — every GoReleaser-built archive since GoReleaser was introduced shipped with the `.gitkeep` placeholder as the embedded frontend instead of the built React app. Root cause: `.goreleaser.yaml` ran `npm run build --prefix web` (output lands in `web/dist/`) but never copied the artefacts into `internal/webui/dist/` where the `go:embed` directive reads from. Both the `Makefile` build and the `Dockerfile` image build had the copy step; the GoReleaser path was the only one that missed it. Fixed by adding an equivalent copy hook. The v0.7.0 **Docker image** (`ghcr.io/vavallee/bindery:v0.7.0`) was **not** affected — this only applies to users who downloaded a binary archive from the v0.7.0 Release page.

### Upgrade notes

- If you downloaded a v0.7.0 binary and saw only `.gitkeep` in the browser, re-download the v0.7.1 archive for your platform. No database migration, no config change.
- Docker / Helm deployments on `ghcr.io/vavallee/bindery:v0.7.0` do not need to move — they were built from a different pipeline and work correctly. Moving to `:v0.7.1` is fine (identical behaviour) but not required.

## [v0.7.0] — 2026-04-14

Polish & onboarding release. Fixes the "added an author, nothing happened for 12 hours" gap that new Sonarr/Radarr migrants hit on day one, fills in the long-broken Series view, and tightens the list-page experience with filters, sorting, and cross-page navigation.

### Fixed

- **Series view always empty** ([#46](https://github.com/vavallee/bindery/issues/46)) — the `/series` endpoint returned nothing because `series` and `series_books` rows were never populated during author ingestion. OpenLibrary's `series` field is now parsed from author-works responses and single-work lookups; after a successful book insert the corresponding `series` row is upserted (by a stable title-derived slug) and a `series_books` link is created with the book's position and primary-series flag.
- **Books page "Sort by newest/oldest" broken** — sort now uses the book's release date rather than its DB insertion timestamp, so chronological ordering matches the year shown in the UI.
- **Books page missing author** — the table now shows an Author column (linked to the author page) and the grid view shows the author name under each cover.

### Added

- **Auto-search on author add** ([#11](https://github.com/vavallee/bindery/issues/11)) — when an author is added with "Start search for books on add" enabled (default on), Bindery immediately fires an indexer search for each wanted book after fetching the author's catalogue from OpenLibrary. Previously users had to wait up to 12 hours for the first automatic grab. The search is gated on the author being monitored; unmonitored authors are unaffected.
- **Auto-search on book status transition to wanted** — updating a book's status to `wanted` (e.g. via "Delete file" → flips imported → wanted, or a manual status edit via the API) now triggers an immediate indexer search. Same logic as the 12-hour scheduler job. Always-on for v0.7.0; a `search_on_status_change` setting can be added later if opt-out is requested.
- **"Start search for books on add" checkbox** in the Add Author modal (default checked), matching Sonarr's phrasing. Uncheck to add an author without an immediate search.
- **`bindery reconcile-series` CLI subcommand** — re-fetches OpenLibrary series data for every already-ingested author and backfills the series/series_books tables. Run once after upgrading from any version that did not populate series during ingestion. Idempotent; prints `{"linked":<n>,"skipped":<n>}` on completion. See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md#from-v06x-to-v070) for usage.
- **Authors page filter by monitored status** — filter chips for `Monitored` / `Unmonitored` / `All`, persisted per-page to localStorage.
- **Author Detail page — publication dates, filters, sort** — books now show a Published column (sortable asc/desc by release date), with filter chips for Type (Ebook/Audiobook), Status (Wanted/Downloaded/Imported/Missing), and Publication state (Released/Upcoming).
- **Book Detail page shows full metadata** — author (linked), series (linked), description, publication date, language.
- **Wanted page navigation** — the book title and author on each Wanted row are now clickable links into the Book Detail and Author Detail pages.

### Changed

- **`NewAuthorHandler` signature** — now takes `*db.SeriesRepo` and `BookSearcher` arguments (added for series upsert and auto-search). Internal API; only callers in `cmd/bindery/main.go` are affected. No REST API change.
- **`NewBookHandler` signature** — now takes a `BookSearcher` argument for the wanted-transition hook. Same scope note.

## [v0.6.4] — 2026-04-14

### Fixed
- **Search returns zero results on many indexers** — two root causes addressed ([#48](https://github.com/vavallee/bindery/pull/48)):
  - *Hardcoded categories:* indexer categories were permanently set to `[7000, 7020]` with no UI to change them. Indexers using non-standard category IDs (e.g. SceneNZBs: **7120** for German books, **3130** for German audio) returned zero results because the `cat=7000,7020` query matched nothing. Both the Add and Edit indexer forms now expose a comma-separated categories input. `filterCategoriesForMedia` already routes 7xxx IDs to ebook searches and 3xxx IDs to audiobook searches — no backend changes needed.
  - *`filterRelevant` phrase-match trap:* a batch-level `anyPhraseMatch` gate disabled keyword fallback for the **entire** result set if any single result happened to have the significant title keywords adjacent. For titles like *"The Name of the Wind"* (`sigWords: ["name","wind"]`), the phrase pattern `\bname\W+wind\b` fails on the correct release because stop words (`"of the"`) sit between the keywords. An abbreviated result (`"Name.Wind.epub"`) could trigger the gate, causing all correctly-titled releases to be dropped. The gate is removed; each result is now evaluated independently — phrase match first, keyword fallback always allowed.

## [v0.6.3] — 2026-04-14

### Fixed
- **Standalone binaries (Windows, macOS, Linux) shipped with no UI** — visiting port 8787 showed only `.gitkeep`. GoReleaser's `before.hooks` only ran `go mod download`; the `npm run build` step ran in the Docker job but not before the cross-compile. Added `npm ci` + `npm run build` to `.goreleaser.yaml` so `internal/webui/dist/` is populated for all release targets ([#44](https://github.com/vavallee/bindery/pull/44)).
- **Protocol-aware download routing** — torznab indexers now route grabs to qBittorrent and newznab indexers route to SABnzbd. Previously the scheduler and manual grab both hardcoded `protocol: "usenet"`, so torrent results were sent to SABnzbd and failed silently ([#41](https://github.com/vavallee/bindery/pull/41)).
- **qBittorrent client form** — the Settings form now shows Username/Password fields for qBittorrent clients (instead of API Key), resets credentials on client-type change, and the Test button dispatches to the correct client type ([#40](https://github.com/vavallee/bindery/pull/40)).
- **Media-type client selection** — when multiple download clients are configured, Bindery now prefers a client whose category contains "audio" for audiobook grabs ([#41](https://github.com/vavallee/bindery/pull/41)).
- **Scan Library button had no feedback** — clicking the button returned silently because the endpoint returns 202 immediately (scan is async). Now shows a green "Scan started" confirmation for 5 seconds.

### Added
- **Per-page size persists across tabs** — the page-size selector in paginated views is stored in `localStorage` per page so the choice survives navigation ([#37](https://github.com/vavallee/bindery/pull/37)).
- **Grab feedback on Wanted page** — the Grab button shows a spinner while the request is in flight and a ✓ check on success before closing the results list ([#38](https://github.com/vavallee/bindery/pull/38)).
- **Manual library scan button** — Settings → General now has a Scan Library button that triggers an immediate background reconciliation ([#39](https://github.com/vavallee/bindery/pull/39)).

### Changed
- Test coverage improved from ~26% to ~33%: new tests for `DownloadRepo`, `BlocklistRepo`, `HistoryRepo`, `UserRepo`, `PickClientForMediaType`, virtual credential round-trips, API handlers (download clients, indexers, tags, library scan), `titleMatch`, `protocolForType`, `dedupe`, `IsArticle`, `ParseMode`, and more.

## [v0.6.2] — 2026-04-14

Bug-fix release on top of v0.6.1.

### Fixed
- **Windows binary exits immediately** ([#7](https://github.com/vavallee/bindery/issues/7)): the default `BINDERY_DB_PATH` was hardcoded to the Linux-container path `/config/bindery.db`. On Windows, `os.MkdirAll("/config", …)` failed, the preflight write probe returned an error, and because the process was spawned from an Explorer double-click the cmd window closed before the user could read the log line. Defaults are now platform-aware via `os.UserConfigDir`: `%APPDATA%\Bindery\bindery.db` on Windows, `~/Library/Application Support/Bindery/bindery.db` on macOS, unchanged `/config/bindery.db` on Linux (existing Docker / Helm / bare-metal deployments are untouched). The resolved paths are emitted in the `"starting bindery"` startup log line so `bindery.exe` runs from `cmd` will surface them even if db.Open later fails.
- **Header nav overflowed into a horizontal scrollbar** at mid viewport widths (≈768–1024px). Desktop nav + version label + sign-out now collapse into the hamburger menu at `lg` (1024px) instead of `md` (768px), and the right-hand cluster is `flex-shrink-0` so it stops being squeezed by the nav tabs.

### Changed
- CI now uploads Go coverage to Codecov (`codecov/codecov-action@v5`) on both the build and validate jobs, with a `.codecov.yml` that marks project/patch checks as informational so coverage dips don't block PRs.

## [v0.6.1] — 2026-04-14

v0.6.1 is the first installable build of the v0.6.0 feature set. The `v0.6.0` tag itself failed GoReleaser cross-compile: `describeDir` referenced `syscall.Stat_t` (POSIX-only) so `GOOS=windows` builds aborted and no binaries or `ghcr.io/vavallee/bindery:0.6.0` image were ever published. See v0.6.0 below for the full feature list.

### Fixed
- Split `describeDir` (the Linux ownership hint in the SQLite "can't open" error path) into `describe_unix.go` (POSIX uid/gid via `syscall.Stat_t`) and `describe_windows.go` (path + mode only) via `//go:build` tags. No runtime behaviour change on Linux; unblocks `windows/amd64` and `windows/arm64` release binaries.

## [v0.6.0] — 2026-04-14

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

### PUID/PGID startup sanity check ([#13](https://github.com/vavallee/bindery/issues/13))

Bindery ships on distroless/static-debian12:nonroot — no shell, no `gosu`, so the container cannot switch user at runtime the way LinuxServer.io images do. The common failure mode is: operator sets `PUID=1000` / `PGID=1000` in their `.env` expecting LSIO semantics, but forgets the matching `--user` / `runAsUser`; Bindery silently runs as UID `65532`, and the first write to `/config` or the library mount fails with an opaque `permission denied`.

This release turns that into a loud, actionable startup error. When `BINDERY_PUID` or `BINDERY_PGID` is set but does not match `os.Getuid()` / `os.Getgid()`, Bindery logs the mismatch along with the exact `docker run --user`, `docker-compose user:`, and `securityContext.runAsUser` snippets that would fix it, then exits non-zero. Leaving both variables unset preserves the previous behaviour (no check, runs as the distroless default UID). Non-Linux builds skip the check entirely (`Getuid` / `Getgid` return `-1` on Windows).

The README's **Configuration → Running as a specific UID/GID** section documents the Docker / compose / k8s patterns end-to-end.

A follow-up ticket (to be opened after v0.6.0) tracks the larger LSIO-style variant image with a gosu entrypoint that actually switches user at runtime — the Bindery team didn't want to ship a second image this cycle.

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

[v1.4.5]: https://github.com/vavallee/bindery/releases/tag/v1.4.5
[v1.4.4]: https://github.com/vavallee/bindery/releases/tag/v1.4.4
[v1.4.3]: https://github.com/vavallee/bindery/releases/tag/v1.4.3
[v1.4.2]: https://github.com/vavallee/bindery/releases/tag/v1.4.2
[v1.4.1]: https://github.com/vavallee/bindery/releases/tag/v1.4.1
[v1.4.0]: https://github.com/vavallee/bindery/releases/tag/v1.4.0
[v1.3.1]: https://github.com/vavallee/bindery/releases/tag/v1.3.1
[v1.3.0]: https://github.com/vavallee/bindery/releases/tag/v1.3.0
[v1.2.7]: https://github.com/vavallee/bindery/releases/tag/v1.2.7
[v1.2.6]: https://github.com/vavallee/bindery/releases/tag/v1.2.6
[v1.2.5]: https://github.com/vavallee/bindery/releases/tag/v1.2.5
[v1.2.4]: https://github.com/vavallee/bindery/releases/tag/v1.2.4
[v1.2.3]: https://github.com/vavallee/bindery/releases/tag/v1.2.3
[v1.2.2]: https://github.com/vavallee/bindery/releases/tag/v1.2.2
[v1.2.1]: https://github.com/vavallee/bindery/releases/tag/v1.2.1
[v1.2.0]: https://github.com/vavallee/bindery/releases/tag/v1.2.0
[v0.19.0]: https://github.com/vavallee/bindery/releases/tag/v0.19.0
[v0.18.3]: https://github.com/vavallee/bindery/releases/tag/v0.18.3
[v0.18.2]: https://github.com/vavallee/bindery/releases/tag/v0.18.2
[v0.18.1]: https://github.com/vavallee/bindery/releases/tag/v0.18.1
[v0.18.0]: https://github.com/vavallee/bindery/releases/tag/v0.18.0
[v0.17.0]: https://github.com/vavallee/bindery/releases/tag/v0.17.0
[v0.16.0]: https://github.com/vavallee/bindery/releases/tag/v0.16.0
[v0.8.0]: https://github.com/vavallee/bindery/releases/tag/v0.8.0
[v0.7.2]: https://github.com/vavallee/bindery/releases/tag/v0.7.2
[v0.7.1]: https://github.com/vavallee/bindery/releases/tag/v0.7.1
[v0.7.0]: https://github.com/vavallee/bindery/releases/tag/v0.7.0
[v0.6.4]: https://github.com/vavallee/bindery/releases/tag/v0.6.4
[v0.6.3]: https://github.com/vavallee/bindery/releases/tag/v0.6.3
[v0.6.2]: https://github.com/vavallee/bindery/releases/tag/v0.6.2
[v0.6.1]: https://github.com/vavallee/bindery/releases/tag/v0.6.1
[v0.6.0]: https://github.com/vavallee/bindery/releases/tag/v0.6.0
[v0.5.2]: https://github.com/vavallee/bindery/releases/tag/v0.5.2
[v0.5.1]: https://github.com/vavallee/bindery/releases/tag/v0.5.1
[v0.5.0]: https://github.com/vavallee/bindery/releases/tag/v0.5.0
[v0.4.2]: https://github.com/vavallee/bindery/releases/tag/v0.4.2
[v0.4.1]: https://github.com/vavallee/bindery/releases/tag/v0.4.1
[v0.4.0]: https://github.com/vavallee/bindery/releases/tag/v0.4.0
[v0.3.0]: https://github.com/vavallee/bindery/releases/tag/v0.3.0
[v0.2.0]: https://github.com/vavallee/bindery/releases/tag/v0.2.0
[v0.1.0]: https://github.com/vavallee/bindery/releases/tag/v0.1.0
