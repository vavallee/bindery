# Roadmap

Tracked feature requests for future releases. Not a commitment — priorities shift based on user feedback and available time. Open an [issue](https://github.com/vavallee/bindery/issues) to propose additions.

The short version lives in the [README](../README.md#roadmap). ✅ items have landed (either in a tagged release or on `development`); ⬜ items are planned. Items with sub-lists track partially-shipped work.

## Planned

- ⬜ **Multi-user support** — per-user libraries, per-user monitored authors, per-user quality profiles.

  Today Bindery assumes a single administrator — the auth schema has a `users` table but is seeded with exactly one row. Multi-user support needs role/permission scoping across the rest of the schema and UI:

  - Author / book / profile rows gain an `owner_user_id` or join-table membership.
  - Handlers filter by the authenticated session's user.
  - The settings page splits into per-user (API key, password, preferences) and admin-only (indexers, download clients, system).
  - Migration from single-user → multi-user re-parents all existing rows to the admin account.

- **OIDC / SSO** — support both deployment shapes so Bindery fits any environment.

  - ⬜ **Native OIDC client** — sign in directly against Authelia / Authentik / Keycloak / Google / GitHub without a reverse proxy in the path. Session cookies from the OIDC flow live alongside the existing username/password and API-key auth; users can mix.
  - ⬜ **Reverse-proxy SSO** — accept upstream-proxy identity headers (`X-Forwarded-User` / `Remote-User`) when auth mode is **Disabled** or the trusted-proxy allowlist is configured. Already the documented workaround today (see the [Reverse-proxy & SSO wiki page](https://github.com/vavallee/bindery/wiki/Reverse-proxy-and-SSO)); formalize it as a first-class path with a trust list so operators don't have to turn auth off wholesale. Overlaps with the [Reverse-proxy header trust](#) item below.

  Goal: the same release supports both homelab users who already run Authelia at the edge **and** users who want to plug OIDC straight into Bindery without standing up a proxy.

- ⬜ **Reverse-proxy header trust** — accept `X-Forwarded-User` / `Remote-User` from a configurable list of trusted upstream proxies so SSO-at-the-edge setups don't require the auth-mode-disabled escape hatch.

  Needs a trust list, header allowlist, and clear docs on the footgun (a misconfigured proxy becomes an auth bypass).

- ⬜ **CSRF tokens** — explicit CSRF token middleware to harden browser flows.

  Session cookies today use `SameSite=Lax`, which blocks cross-site form posts. On the list for a subsequent hardening pass.

- ⬜ **External database support (MySQL / Postgres)** ([#86](https://github.com/vavallee/bindery/issues/86)) — optional settings for DB host, credentials, and connection path so Bindery can run against a shared MySQL/Postgres instance instead of the bundled SQLite file.

  Useful for multi-replica HA deployments. Planned to ship alongside multi-user support — a single-user instance has no concurrency pressure that justifies leaving SQLite, and bundling both avoids two separate schema migrations.

- **UI localization (i18n)** — translate the web UI into French, Dutch, and German (starting point; more languages welcome as contributors show up).
  - ✅ Translation-catalogue extraction pass (landed in v0.12.0).
  - ✅ Runtime switcher (language selector in Settings, persisted in `localStorage` so it applies before first paint alongside the theme).
  - ✅ Locale-aware date/number formatting.
  - ✅ `Accept-Language` auto-detect on first load with manual override.

- ✅ **Direct title/keyword search** ([#85](https://github.com/vavallee/bindery/issues/85), [#267](https://github.com/vavallee/bindery/issues/267), landed in development) — Search page at `/search` lets users search all enabled indexers by title, author, or keyword directly from the nav without adding an author first. Results display inline with a Grab button. Backend endpoint (`GET /api/v1/indexer/search`) already existed; PR #266 added the dedicated UI.

- **Cover image privacy / local caching** — prevent the browser from contacting third-party image hosts (Goodreads, OpenLibrary, Google Books) directly, which would leak the user's IP and reading habits.
  - ✅ **Server-side image proxy cache (closes [#112](https://github.com/vavallee/bindery/issues/112), landed in development)** — `GET /api/v1/images?url=<encoded>` fetches and caches cover images under `<dataDir>/image-cache/` with a 30-day TTL. All `imageURL` fields in API responses are rewritten to this proxy path before leaving the server. No browser-to-third-party requests, no fingerprinting.

- **Non-English indexer / metadata support** — let monitored authors and searches pull from language-tagged catalogues and filter results by language.

  - ✅ Per-author metadata profiles carry an `allowed_languages` list; OpenLibrary works whose language falls outside it are dropped during author ingestion ([#14](https://github.com/vavallee/bindery/issues/14), landed in v0.6.0).
  - ✅ Propagate the profile's languages into indexer queries (Prowlarr's `Categories` + language filters, Jackett `/api?cat=7000&...`) so Newznab-side filtering applies (landed in v0.12.0).
  - ✅ Surface the language tag in search-result and wanted-books views.
  - ✅ Persist Hardcover/Google Books' `language` field for editions.
  - ✅ **DNB (Deutsche Nationalbibliothek) metadata provider** ([#67](https://github.com/vavallee/bindery/issues/67), landed in development) — public SRU endpoint (`services.dnb.de/sru/dnb`) with MARC21-XML record schema; no API key. Always-on enricher alongside Hardcover. Fills description, language, year, publisher from MARC fields. Especially useful for German-language titles where OpenLibrary / Google Books coverage is thin.

- ~~**LinuxServer.io-style runtime user switching** ([#56](https://github.com/vavallee/bindery/issues/56))~~ — **Won't do.** The distroless image is deliberately minimal (no shell, no `gosu`). Runtime UID/GID switching requires a shell entrypoint, which contradicts the minimal-attack-surface posture. Pass `--user <uid>:<gid>` to `docker run` or set `securityContext.runAsUser` in Helm. Closed as won't-fix.

- **Import mode — move / copy / hardlink** ([#54](https://github.com/vavallee/bindery/issues/54))
  - ✅ **Move / Copy / Hardlink** (landed in v0.12.0) — configurable under **Settings → General → Import Mode**. Hardlink requires the download dir and library on the same filesystem. Copy preserves the source so torrent clients continue seeding.

- **Calibre library integration** — treat a Calibre library as a first-class storage target, for users who already live in Calibre or want e-reader sync.

  The user-facing goal: a monitored author releases a new book, Bindery finds and grabs it, and the result lands in Calibre under the existing author automatically — no manual "Add books" step.

  - ✅ **Path A — `calibredb` post-import hook** ([#32](https://github.com/vavallee/bindery/issues/32), landed in v0.8.0) — every successful Bindery import is mirrored into the configured Calibre library by shelling out to `calibredb add --with-library <path>`. The returned Calibre book id is persisted on the Bindery book row so future OPDS / sync work has a stable handle. Opt-in via Settings → General → Calibre (enabled / library path / binary path) with a Test connection button.
  - ✅ **Library import & sync** ([#63](https://github.com/vavallee/bindery/issues/63), landed in v0.9.0) — reads an existing Calibre library's `metadata.db` directly (pure Go, no CGO, read-only) and ingests it as Bindery's catalogue. Three-tier dedup (by Calibre id → title+author → insert new) makes re-imports idempotent. Co-authors become alias rows. Trigger via **Settings → General → Calibre → Import library** or `calibre.sync_on_startup`.
  - ~~**Path B — Calibre-watched drop folder** ([#64](https://github.com/vavallee/bindery/issues/64), landed in v0.9.0, **removed in v0.17.0**)~~ — The drop-folder mode copied files into a Calibre-watched directory and polled `metadata.db` for the resulting book id. **This mode has been removed** because it fundamentally depends on the Calibre GUI application running and its auto-add watcher being active. In a containerised / headless deployment (the primary Bindery use case), the Calibre GUI is not reliably open, so the watcher never fires and books silently time out. The `calibredb` mode achieves the same result without any of these constraints — it requires only that both Bindery and Calibre share the library directory via a volume mount, which is already required for the library import/sync feature.
  - ✅ **Configurable per-library mode** ([#64](https://github.com/vavallee/bindery/issues/64), landed in v0.9.0) — Settings → General → Calibre exposes a mode selector: **Off** or **calibredb CLI**. Toggling takes effect without a restart.
  - ✅ **OPDS feed** ([#65](https://github.com/vavallee/bindery/issues/65), landed in v0.9.0) — OPDS 1.2 Atom catalogue at `/opds/v1.2/` so KOReader / Moon+ Reader / etc. can browse and download without running Calibre itself. Authenticated with HTTP Basic Auth (API key as password).

## v2 horizon

These items are too large or architectural for a minor release. They define the v2 milestone — the set of changes that would warrant a major version bump.

- **Multi-user with role separation** — Full multi-tenant model: every author, book, profile, and download history row is scoped to an owner. Admin role retains global access (indexers, download clients, system settings). Library overlap handled by shared "global" authors that any user can monitor. Needs schema migration, API layer changes, and a rewritten Settings page split into per-user and admin sections. Blocked on the token-based OIDC work below (need identity from multiple providers before multi-user makes sense).

- **Native OIDC / SSO with multi-provider discovery** — Sign in against Authelia, Authentik, Keycloak, Google, or GitHub natively without an external reverse proxy. Session tokens issued by Bindery after validating the OIDC callback. Overlaps with the multi-user story: each OIDC subject maps to a Bindery user row.

- **External database (MySQL / Postgres)** ([#86](https://github.com/vavallee/bindery/issues/86)) — The current `modernc.org/sqlite` driver is zero-CGO and ships inside the binary, which is excellent for single-instance homelabs. Multi-replica HA requires a shared external store. SQLite WAL is not a substitute for row-level locking under concurrent writers. The schema is already designed with foreign keys and explicit transactions; porting to `database/sql` + a MySQL/Postgres driver is feasible but requires end-to-end testing against both engines, a migration planner that works per-engine, and probably a connection-pool configurator in Settings.

  > **Ships with multi-user.** External DB support only makes sense alongside multi-user (#73 above) — a single-user instance has no concurrency pressure that justifies leaving SQLite. Plan to deliver both in the same release so the migration path is tested once, not twice.

- **Persistent structured log store** — The current ring buffer (1 000 entries, in-process memory) is a good v1 for the log viewer (Settings → Logs, [#93](https://github.com/vavallee/bindery/issues/93)). A v2 log store would persist entries to the database (or a rolling log file), survive restarts, be queryable across date ranges, and support structured search. Useful for incident retrospectives on long-running instances.

## Explicitly out of scope

These get asked often enough to warrant a standing answer. They're not on the roadmap and new issues requesting them will be closed with a link here.

### Z-Library / Anna's Archive / LibGen / other shadow libraries

Bindery's search pipeline is built on **documented, stable public APIs** — Newznab, Torznab, OpenLibrary, Google Books, Hardcover. Shadow libraries don't fit that posture:

- **Legal risk** — hosting integration code against a service under active copyright litigation exposes the project and anyone running it. The *arr ecosystem's deliberate distance from these sources is the same call.
- **API instability** — shadow-library endpoints move, rename, get seized, and return in different forms. The "documented, stable" test exists specifically to keep Readarr's `api.bookinfo.club` failure mode from recurring.
- **Search quality** — these services don't publish structured metadata (no foreign-book-id mapping back to OpenLibrary works), so results can't be ranked against the quality-profile / edition / language machinery that drives the rest of Bindery.

If you need these sources, point a [Jackett](https://github.com/Jackett/Jackett) / [Prowlarr](https://github.com/Prowlarr/Prowlarr) instance at them and wire that into Bindery via Torznab. The indexer layer is a proxy boundary by design — what lives behind it is the operator's choice.

### OpenBooks / IRC #ebooks integration

[OpenBooks](https://github.com/evan-buss/openbooks) (IRC-based ebook retrieval from `#ebooks` on IRCHighway) is a great tool but doesn't compose with Bindery's architecture:

- **Protocol mismatch** — IRC DCC transfers are stateful, session-oriented, and manual (`@search` → results → `!<bot> <filename>`). Bindery's fire-and-forget grab → queue → import pipeline assumes an HTTP-fetchable URL (NZB, `.torrent`, magnet).
- **No result metadata** — IRC search results are filenames, not structured release objects with size / pub-date / grabs / indexer ID. The ranker and custom-format matchers would degenerate to substring matching.
- **Maintenance burden** — IRC bots rotate, channel rules change, trigger syntax drifts. Absorbing that churn into the release pipeline isn't in scope for a single-maintainer project.

Run OpenBooks alongside Bindery for one-off lookups — it's a different tool with a different shape, and pretending otherwise degrades both.
