# Changelog

All notable changes to Bindery are documented here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com) and versions follow
[Semantic Versioning](https://semver.org).

## [Unreleased] — development branch

The `development` branch carries the in-flight feature set for the next release. Images are published as `ghcr.io/vavallee/bindery:development` and `:dev-<sha>`; point ArgoCD at the `development` branch to follow. Treat these features as beta — schema migrations are additive and safe, but UX may still shift before tagging.

### Fixed

- **NZB grabs misrouted to qBittorrent** (#320) — Prowlarr-synced indexers were hardcoded as `torznab` regardless of the upstream indexer's actual protocol, so NZB search results were tagged `protocol=torrent` and dispatched to qBittorrent, which then failed with `add torrent accepted but hash could not be determined`. The syncer now uses Prowlarr's `protocol` field to choose `newznab` vs `torznab`, and corrects mis-typed rows on the next sync. The scheduler no longer falls back across protocols when the protocol-matched client list is empty — an NZB release can never be pushed to a torrent client.

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
