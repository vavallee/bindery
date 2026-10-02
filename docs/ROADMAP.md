# Roadmap

What's being worked on, what's likely next, and what has already shipped. Not a commitment — priorities shift based on user feedback and available time. Open an [issue](https://github.com/vavallee/bindery/issues) to propose additions.

Most of what ships is driven by issues and Discord reports rather than by this document. The **Now** and **Next up** sections are the parts worth reading; everything below them is history and standing answers.

## Now

Work in flight for the next point release.

Nothing is pinned here right now. Day to day work is driven by the [issue tracker](https://github.com/vavallee/bindery/issues); the last item this section carried, unattended release discovery, shipped in v1.37.0 and has moved to **Delivered** below.

## Next up

Real candidates, not commitments, roughly in order of how much they'd change the product.

- **Series from file path** ([#1430](https://github.com/vavallee/bindery/issues/1430)): the last open piece of bringing an existing library in. Adopting what a library scan could not match, the Manual Import wizard, creating a book from an unmatched file, and bulk folder import have all shipped (see **Delivered**). Importing an existing library is still the single biggest source of support threads.

- **Series discovery** ([#2523](https://github.com/vavallee/bindery/issues/2523)): a monitored series finds its own new books. Waits on telling a new work from a new edition ([#2524](https://github.com/vavallee/bindery/issues/2524)).

- **Merge books / multiple editions** ([#1358](https://github.com/vavallee/bindery/issues/1358)) — group alternate and non-English editions under one book instead of minting duplicates. Real design work; matters most to non-English users.

- **slskd as a download client** ([#1717](https://github.com/vavallee/bindery/issues/1717)) — see the note under Won't do; the re-scoped version is an ordinary download client and shares its shape with the rTorrent client that shipped in v1.31.0.

- **Dual-format storage and routing** — keep multiple formats rather than upgrade-replacing ([#1357](https://github.com/vavallee/bindery/issues/1357)). Routing per format on import, so a Calibre/CWA + Audiobookshelf split can be expressed, shipped as the audiobook import mode and drop folder ([#1632](https://github.com/vavallee/bindery/issues/1632)). Handling dual-format properly is Bindery's clearest advantage over running two Readarr instances, so this cluster is worth doing as a set. Sharing one book folder between the two formats already shipped (see **Delivered**).

- **Ebook language handling** ([#1160](https://github.com/vavallee/bindery/issues/1160)) — make a book's language filterable in the library, and stop authors pulling in foreign-language editions. Editing a book's language by hand (v1.25.0) and reading `dc:language` from the imported EPUB (v1.28.0) have shipped.

## Delivered

Condensed; see [CHANGELOG.md](../CHANGELOG.md) for the full history.

**Auth and multi-user** — Multi-user support with per-user libraries, monitored authors and quality profiles (v1.0.0/v1.0.1). Native OIDC client for Authelia / Authentik / Keycloak / Google / GitHub without a proxy in the path, plus reverse-proxy SSO accepting upstream identity headers from a configured trusted-proxy CIDR list (both v1.0.0; see the [Reverse-proxy & SSO wiki page](https://github.com/vavallee/bindery/wiki/Reverse-proxy-and-SSO)). CSRF double-submit tokens on all session-cookie mutations, API-key clients exempt (v1.0.0; see [Use CSRF tokens in scripts](https://github.com/vavallee/bindery/wiki/Howto-CSRF-tokens)).

**Calibre and e-reader integration** — `calibredb` post-import hook mirroring every import into a Calibre library ([#32](https://github.com/vavallee/bindery/issues/32), v0.8.0). Direct read-only ingest of an existing Calibre `metadata.db` as Bindery's catalogue, with three-tier idempotent dedup ([#63](https://github.com/vavallee/bindery/issues/63), v0.9.0). Per-library mode selector ([#64](https://github.com/vavallee/bindery/issues/64), v0.9.0). OPDS 1.2 catalogue at `/opds/` for KOReader, Moon+ Reader and friends ([#65](https://github.com/vavallee/bindery/issues/65), v0.9.0). Calibre-Web-Automated ingest folder ([#417](https://github.com/vavallee/bindery/issues/417), v1.9.0), later extended by the general post-import drop folder ([#941](https://github.com/vavallee/bindery/issues/941), v1.17.0).

The Calibre-watched drop folder ([#64](https://github.com/vavallee/bindery/issues/64)) shipped in v0.9.0 and was **removed in v0.17.0**: it depended on the Calibre GUI running with its auto-add watcher active, which never holds in a headless container, so books silently timed out. The `calibredb` mode reaches the same result needing only a shared library volume.

**Search and metadata** — Direct title/keyword search page ([#85](https://github.com/vavallee/bindery/issues/85), [#267](https://github.com/vavallee/bindery/issues/267), v0.20.0). Split ebook/audiobook search results ([#333](https://github.com/vavallee/bindery/issues/333), v1.2.x). Non-English metadata: per-author `allowed_languages` filtering during ingestion ([#14](https://github.com/vavallee/bindery/issues/14), v0.6.0), language propagation into Prowlarr/Jackett queries (v0.12.0), language tags in result views, and edition-level language from Hardcover and Google Books. DNB (Deutsche Nationalbibliothek) provider over the public SRU/MARC21 endpoint, no API key ([#67](https://github.com/vavallee/bindery/issues/67), v0.x, deepened through v1.27.0).

**Acquisition and monitoring**: One search-first **Add to library** dialog for authors and books, with library matches marked in place, plus a header search over your own catalogue that hands misses to it ([#1227](https://github.com/vavallee/bindery/issues/1227), [#2551](https://github.com/vavallee/bindery/issues/2551), v1.36.0). Adding one book no longer imports the author's back catalogue, and an author refresh respects monitor mode ([#1816](https://github.com/vavallee/bindery/issues/1816), [#1815](https://github.com/vavallee/bindery/issues/1815), v1.31.0). Hardcover list sync runs as a background job instead of inside the request, with a configurable interval ([#1854](https://github.com/vavallee/bindery/issues/1854), [#1848](https://github.com/vavallee/bindery/issues/1848), v1.31.0).

**Bringing a library in**: Bulk folder import ([#1292](https://github.com/vavallee/bindery/issues/1292), v1.22.2). Manual Import wizard ([#1236](https://github.com/vavallee/bindery/issues/1236), v1.28.0), which can also create a book from an unmatched file ([#1719](https://github.com/vavallee/bindery/issues/1719), v1.33.0). Library scan matching for dual-format books: contributor-list Artist tags, both files of a dual-format folder claimed in one pass, and an unmatched-files hint that names what actually failed ([#1956](https://github.com/vavallee/bindery/issues/1956), [#1957](https://github.com/vavallee/bindery/issues/1957), [#1958](https://github.com/vavallee/bindery/issues/1958), v1.31.0). Format-scoped already-in-library check, so importing one format never abandons the other format's download ([#1885](https://github.com/vavallee/bindery/issues/1885), v1.31.0). An ebook and audiobook share one book folder under the shared-folder layout instead of appending ` (2)` ([#1959](https://github.com/vavallee/bindery/issues/1959), v1.33.3). Adopting the books a library scan could not match: they wait on the Import page as one row per book, an audiobook folder or disc set counting as one row, with suggested matches, one Add author decision per missing author, Undo, and the files registered where they already are ([#2547](https://github.com/vavallee/bindery/issues/2547), v1.37.0).

**Download clients**: rTorrent / ruTorrent over XML-RPC, HTTP or SCGI ([#1618](https://github.com/vavallee/bindery/issues/1618), v1.31.0). Diagnose on each saved download client: one click walks the connection, the category, where ebook and audiobook grabs land, the path remap, folder readability and whether imports can hardlink, then names the first thing to fix (refs [#2031](https://github.com/vavallee/bindery/issues/2031), v1.37.0).

**Sharing an instance**: a requester role that can browse the library read only and ask for a book or an author, with an admin Requests queue, a status each requester can follow, and a `requestCreated` webhook (v1.37.0). See [docs/multi-user.md](multi-user.md#requester).

**Unattended release discovery**: monitored authors are checked for new books on a schedule, off until you pick Daily, Weekly or Monthly in Settings, General, New release discovery, plus a `bookAnnounced` webhook listing what a refresh or a scheduled check added ([#2236](https://github.com/vavallee/bindery/issues/2236), v1.37.0).

**Storage and privacy** — Import modes move / copy / hardlink ([#54](https://github.com/vavallee/bindery/issues/54), v0.12.0). Configurable default root folder ([#332](https://github.com/vavallee/bindery/issues/332), v1.2.0). Server-side cover-image proxy and cache so the browser never contacts third-party image hosts ([#112](https://github.com/vavallee/bindery/issues/112), ~v0.16.0). Persistent structured log store in SQLite, queryable and retention-bounded ([#241](https://github.com/vavallee/bindery/issues/241), v1.2.x). Download the filtered log as a text file from Settings → Logs ([#1903](https://github.com/vavallee/bindery/issues/1903), v1.31.0). Live warning at the Import Mode selector when Hardlink is picked on a setup that cannot hardlink ([#1720](https://github.com/vavallee/bindery/issues/1720), v1.31.0).

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
