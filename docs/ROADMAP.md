# Roadmap

Tracked feature requests for future releases. Not a commitment — priorities shift based on user feedback and available time. Open an [issue](https://github.com/vavallee/bindery/issues) to propose additions.

The short version lives in the [README](../README.md#roadmap). The long version below covers scope, motivation, and implementation trade-offs for each item.

## Multi-user support

Per-user libraries, per-user monitored authors, per-user quality profiles. Today Bindery assumes a single administrator — the auth schema has a `users` table but is seeded with exactly one row. Multi-user support needs role/permission scoping across the rest of the schema and UI:

- Author / book / profile rows gain an `owner_user_id` or join-table membership.
- Handlers filter by the authenticated session's user.
- The settings page splits into per-user (API key, password, preferences) and admin-only (indexers, download clients, system).
- Migration from single-user → multi-user re-parents all existing rows to the admin account.

## OIDC / SSO

Plug a native OIDC client in alongside the existing session/API-key flow (Authelia, Authentik, Keycloak, Google, GitHub). Currently the suggested path is to put Bindery behind a reverse proxy that terminates SSO and set the auth mode to **Disabled** on the internal network — see the [Reverse-proxy & SSO wiki page](https://github.com/vavallee/bindery/wiki/Reverse-proxy-and-SSO).

## Reverse-proxy header trust

Accept `X-Forwarded-User` / `Remote-User` from a configurable list of trusted upstream proxies so SSO-at-the-edge setups don't require the auth-mode-disabled escape hatch. Needs a trust list, header allowlist, and clear docs on the footgun (a misconfigured proxy becomes an auth bypass).

## CSRF tokens

Session cookies today use `SameSite=Lax`, which blocks cross-site form posts. Adding an explicit CSRF token middleware would harden browser flows further and is on the list for a subsequent hardening pass.

## External database support (MySQL / Postgres)

Optional settings for DB host, credentials, and connection path so Bindery can run against a shared MySQL/Postgres instance instead of the bundled SQLite file. Useful for multi-replica HA deployments.

## UI localization (i18n)

Translate the web UI into French, Dutch, and German (starting point; more languages welcome as contributors show up). Today all labels, button text, error messages, and toasts are hardcoded English strings. Needs:

- A translation-catalogue extraction pass.
- A small runtime switcher (language selector in Settings, persisted in `localStorage` so it applies before first paint alongside the theme).
- Locale-aware date/number formatting.
- `Accept-Language` auto-detect on first load with manual override.

## Non-English indexer / metadata support

Let monitored authors and searches pull from language-tagged catalogues and filter results by language.

**Landed in v0.6.0** ([#14](https://github.com/vavallee/bindery/issues/14)): per-author metadata profiles carry an `allowed_languages` list, and OpenLibrary works whose language falls outside it are dropped during author ingestion.

**Remaining:**

- Propagate the profile's languages into indexer queries (Prowlarr's `Categories` + language filters, Jackett `/api?cat=7000&...`) so Newznab-side filtering applies.
- Surface the language tag in search-result and wanted-books views.
- Persist Hardcover/Google Books' `language` field for editions.

Relevant to French/Dutch/German users whose libraries are mixed-language and where indexer results in the "wrong" language are currently indistinguishable.

## LinuxServer.io-style runtime user switching

A parallel image with a gosu/su-exec entrypoint that switches UID/GID at runtime based on `PUID` / `PGID`. The current distroless image is deliberately minimal (no shell, no `gosu`) — the v0.6.0 startup sanity check ([#13](https://github.com/vavallee/bindery/issues/13)) catches PUID/PGID misconfiguration but does not fix it.

Trade-offs:

- Distroless image: smaller, smaller attack surface, no runtime user-switching → the user has to pass `--user`.
- LSIO-style image: larger, needs shell + gosu, but "just works" for users coming from the *arr ecosystem.

The likely path is to publish **both** and let operators pick.

## Calibre library integration

Treat a Calibre library as a first-class storage target, for users who already live in Calibre or want e-reader sync. The user-facing goal: a monitored author releases a new book, Bindery finds and grabs it, and the result lands in Calibre under the existing author automatically — no manual "Add books" step.

### Library import & sync

On startup, read an existing Calibre library (`metadata.opf` + `Author/Title (id)/…` folder layout) and ingest it as Bindery's catalogue. Detect out-of-band Calibre edits and re-sync.

### Path A — Direct write-through to `metadata.db` (tightest integration)

On every successful import, insert/update the row in Calibre's SQLite `metadata.db` so the new book appears in Calibre immediately (no watcher, no scan). Place the file under the existing author folder Calibre already knows about (`Author Name/Book Title (id)/book.ext`) and write the matching sidecars (`metadata.opf`, `cover.jpg`). Matches what the *arr → Calibre workflow currently requires a plugin for.

### Path B — Calibre-watched drop folder (looser coupling)

Alternative for users who'd rather let Calibre do its own ingestion (the [Calibre-Web-Automated](https://github.com/crocodilestick/Calibre-Web-Automated) pattern): Bindery drops finished files into a configured watch directory, Calibre auto-adds them, and Bindery then reads the library back to discover the new book row and link it to the originating grab / history entry. Requires less coordination with Calibre internals, at the cost of a moment of uncertainty while Calibre processes the file.

### Configurable per-library mode

Expose both paths as settings so users pick the one that matches their setup. Default to path B (drop folder) because it's the safer option against Calibre schema changes.

### OPDS feed

Expose a Calibre-content-server-style OPDS endpoint so KOReader / Moon+ Reader / etc. can browse and download without running Calibre itself.

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
