# Roadmap

What's being worked on, what's likely next, and what has already shipped. Not a commitment — priorities shift based on user feedback and available time. Open an [issue](https://github.com/vavallee/bindery/issues) to propose additions.

Most of what ships is driven by issues and Discord reports rather than by this document. The **Now** and **Next up** sections are the parts worth reading; everything below them is history and standing answers.

## Now

Work in flight for the next point release.

- **Library scan matching for dual-format books** ([#1956](https://github.com/vavallee/bindery/issues/1956), [#1957](https://github.com/vavallee/bindery/issues/1957), [#1958](https://github.com/vavallee/bindery/issues/1958)) — an audiobook whose Artist tag carries a contributor list (`Author, Translator - translator, Narrator`, the standard Audible shape) can never match its catalogue author, so it sits in Unmatched forever. Alongside it: a scan claims only one file per book per pass, so a folder holding both an epub and an m4b needs two full scans; and the unmatched-files hint blames the author catalogue even when the catalogue is fine.

- **Format-scoped already-in-library check** ([#1885](https://github.com/vavallee/bindery/issues/1885)) — when the audiobook of a title imports first, the still-pending ebook download of the same title is marked imported and abandoned. Each title's ebook and audiobook slots are independent pipelines and an import of one must never satisfy the other.

- **Hardcover list sync off the request lifetime** ([#1854](https://github.com/vavallee/bindery/issues/1854), [#1848](https://github.com/vavallee/bindery/issues/1848)) — manual "Sync now" runs inside the HTTP request, so it's capped by the 60s request timeout; on a large shelf most of the list never imports. Moving it to a background job removes the ceiling. The fixed 24h sync interval becomes configurable at the same time.

- **Stop unwanted back-catalogue imports** ([#1816](https://github.com/vavallee/bindery/issues/1816), [#1815](https://github.com/vavallee/bindery/issues/1815)) — adding or searching for a single book can pull in the author's entire catalogue, and refreshing an author does it even with monitor mode set to None. Monitor mode should be authoritative, and "add this book" should mean only that book.

- **rTorrent / ruTorrent download client** ([#1618](https://github.com/vavallee/bindery/issues/1618)) — the most-requested missing download client.

- **Support-cost reducers** — download log files from the UI ([#1903](https://github.com/vavallee/bindery/issues/1903)) so users on rootless containers can attach logs to issues, and a live hardlink warning at the Import Mode selector ([#1720](https://github.com/vavallee/bindery/issues/1720)) instead of only down in the Storage section.

## Next up

Real candidates, not commitments, roughly in order of how much they'd change the product.

- **Manual Import wizard** ([#1236](https://github.com/vavallee/bindery/issues/1236)) and its dead-end on unmatched files ([#1719](https://github.com/vavallee/bindery/issues/1719)) — matching each unmatched file to a new or existing book, with edition and profile choice. Together with bulk folder import ([#1292](https://github.com/vavallee/bindery/issues/1292)) and series-from-path ([#1430](https://github.com/vavallee/bindery/issues/1430)) this is one surface: bringing an existing library in. It's the single biggest source of support threads, so it deserves a full cycle rather than a corner of one.

- **Merge books / multiple editions** ([#1358](https://github.com/vavallee/bindery/issues/1358)) — group alternate and non-English editions under one book instead of minting duplicates. Real design work; matters most to non-English users.

- **Unify add and search acquisition flows** ([#1227](https://github.com/vavallee/bindery/issues/1227)) — search-first Add Author / Add Book with configuration on a confirm step.

- **slskd as a download client** ([#1717](https://github.com/vavallee/bindery/issues/1717)) — see the note under Won't do; the re-scoped version is an ordinary download client and shares its shape with the rTorrent work above.

- **Dual-format storage and routing** — keep multiple formats rather than upgrade-replacing ([#1357](https://github.com/vavallee/bindery/issues/1357)), let an ebook and audiobook share one book folder instead of appending ` (2)` ([#1959](https://github.com/vavallee/bindery/issues/1959)), and route per-format on external import so a Calibre/CWA + Audiobookshelf split can be expressed ([#1632](https://github.com/vavallee/bindery/issues/1632)). Handling dual-format properly is Bindery's clearest advantage over running two Readarr instances, so this cluster is worth doing as a set.

- **Ebook language handling** ([#1160](https://github.com/vavallee/bindery/issues/1160)) — read `dc:language`, make it editable and filterable, and stop authors pulling in foreign-language editions.

## Delivered

Condensed; see [CHANGELOG.md](../CHANGELOG.md) for the full history.

**Auth and multi-user** — Multi-user support with per-user libraries, monitored authors and quality profiles (v1.0.0/v1.0.1). Native OIDC client for Authelia / Authentik / Keycloak / Google / GitHub without a proxy in the path, plus reverse-proxy SSO accepting upstream identity headers from a configured trusted-proxy CIDR list (both v1.0.0; see the [Reverse-proxy & SSO wiki page](https://github.com/vavallee/bindery/wiki/Reverse-proxy-and-SSO)). CSRF double-submit tokens on all session-cookie mutations, API-key clients exempt (v1.0.0; see [Use CSRF tokens in scripts](https://github.com/vavallee/bindery/wiki/Howto-CSRF-tokens)).

**Calibre and e-reader integration** — `calibredb` post-import hook mirroring every import into a Calibre library ([#32](https://github.com/vavallee/bindery/issues/32), v0.8.0). Direct read-only ingest of an existing Calibre `metadata.db` as Bindery's catalogue, with three-tier idempotent dedup ([#63](https://github.com/vavallee/bindery/issues/63), v0.9.0). Per-library mode selector ([#64](https://github.com/vavallee/bindery/issues/64), v0.9.0). OPDS 1.2 catalogue at `/opds/` for KOReader, Moon+ Reader and friends ([#65](https://github.com/vavallee/bindery/issues/65), v0.9.0). Calibre-Web-Automated ingest folder ([#417](https://github.com/vavallee/bindery/issues/417), v1.9.0), later extended by the general post-import drop folder ([#941](https://github.com/vavallee/bindery/issues/941), v1.18.0).

The Calibre-watched drop folder ([#64](https://github.com/vavallee/bindery/issues/64)) shipped in v0.9.0 and was **removed in v0.17.0**: it depended on the Calibre GUI running with its auto-add watcher active, which never holds in a headless container, so books silently timed out. The `calibredb` mode reaches the same result needing only a shared library volume.

**Search and metadata** — Direct title/keyword search page ([#85](https://github.com/vavallee/bindery/issues/85), [#267](https://github.com/vavallee/bindery/issues/267), v0.20.0). Split ebook/audiobook search results ([#333](https://github.com/vavallee/bindery/issues/333), v1.2.x). Non-English metadata: per-author `allowed_languages` filtering during ingestion ([#14](https://github.com/vavallee/bindery/issues/14), v0.6.0), language propagation into Prowlarr/Jackett queries (v0.12.0), language tags in result views, and edition-level language from Hardcover and Google Books. DNB (Deutsche Nationalbibliothek) provider over the public SRU/MARC21 endpoint, no API key ([#67](https://github.com/vavallee/bindery/issues/67), v0.x, deepened through v1.27.0).

**Storage and privacy** — Import modes move / copy / hardlink ([#54](https://github.com/vavallee/bindery/issues/54), v0.12.0). Configurable default root folder ([#332](https://github.com/vavallee/bindery/issues/332), v1.2.1). Server-side cover-image proxy and cache so the browser never contacts third-party image hosts ([#112](https://github.com/vavallee/bindery/issues/112), ~v0.16.0). Persistent structured log store in SQLite, queryable and retention-bounded ([#241](https://github.com/vavallee/bindery/issues/241), v1.2.x).

**UI** — Full i18n: catalogue extraction (v0.12.0), runtime language switcher persisted before first paint, locale-aware date/number formatting, and `Accept-Language` auto-detect with manual override. Editable quality profiles with a full create/rename/delete editor; note the format allow-list was UI-only until v1.28.2 actually enforced it ([#1693](https://github.com/vavallee/bindery/issues/1693)).

## Won't do

- **External database (MySQL / Postgres)** ([#86](https://github.com/vavallee/bindery/issues/86)) — SQLite with WAL mode handles all realistic single-instance and multi-user load. An external database server adds credentials, backups, version management and connection pooling for no concrete homelab benefit.

- **LinuxServer.io-style runtime user switching** ([#56](https://github.com/vavallee/bindery/issues/56)) — the distroless image has no shell and no `gosu` on purpose. Runtime UID/GID switching needs a shell entrypoint, which contradicts the minimal-attack-surface posture. Pass `--user <uid>:<gid>` to `docker run` or set `securityContext.runAsUser` in Helm.

- **Implementing the Soulseek `slsk://` protocol directly** ([#646](https://github.com/vavallee/bindery/issues/646)) — stateful, session-oriented peer protocol (handshake, peer discovery, queue, slot management) requiring a long-lived peer connection inside the Bindery process and a search-result schema rich enough to carry peer/file metadata through the ranker. That was the version this roadmap deferred to a v2 horizon, and it stays declined.

  What replaced it: [#1717](https://github.com/vavallee/bindery/issues/1717) treats **slskd** as a download client rather than a protocol to implement. slskd is already the sidecar, exposing a REST API with key auth, search, and `transfers/downloads` — the same enqueue-then-poll shape `internal/downloader/adapter.go` assumes for every other client. It's in **Next up** above.

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

### Magazines / periodicals

Bindery manages books. Magazines look adjacent but break the data model at every layer:

- **No metadata source** — every provider Bindery uses (Hardcover, OpenLibrary, Google Books, Audible, Audnexus, DNB) is a *book* identity system, keyed on works, editions, and ISBNs. Periodicals carry ISSNs and issue numbers, and none of these publish issue-level catalogues. There is nothing to monitor an author's catalogue *against*.
- **Identity mismatch** — Bindery's spine is Author → Book → Edition. A magazine has no author; the unit is a recurring title with dated issues, and the series machinery models reading order within a finite work, not an open-ended publication schedule.
- **Unbounded monitoring** — an author's catalogue is finite and can be refreshed to completion. A periodical never completes, so "monitor" would need a different scheduler contract, a different wanted-list semantic, and a different definition of "done".

Supporting this properly means a second identity model living alongside the first, not a new format flag. That's a fork's worth of work for a single-maintainer project.

Note that this is about *periodicals*, not *file formats* — `cbr` and `cbz` are already recognised release formats, so a graphic novel released as a normal book is handled like any other book.
