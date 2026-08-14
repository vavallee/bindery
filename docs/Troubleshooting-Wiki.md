# Troubleshooting

Solutions to recurring problems, organised by symptom. Add new entries here as patterns come up in support.

## Bindery will not start after upgrading: "foreign_key_check found N violation(s)"

```
ERROR msg=failed to open database error=run migrations: migration 72: foreign_key_check found 2231 violation(s)
```

Affects **v1.30.1, v1.30.2 and v1.30.3**. Fixed in **v1.30.4** — upgrading is the whole fix, and nothing else is required.

**Your data is fine.** Migrations run in a transaction, so the failed migration rolled back and the database on disk is exactly as it was. Repulling the image does not help, because nothing is wrong with the image: the condition lives in your config volume.

What happened: migration 72 rebuilds two Calibre tables, and its post-rebuild integrity check scanned the **whole** database instead of the tables it rebuilt. Long-running instances carry orphan rows left over from [#1727](https://github.com/vavallee/bindery/issues/1727), where foreign key enforcement could silently switch off and the schema's `ON DELETE CASCADE` rules stopped firing. Those old, unrelated orphans failed a migration that had nothing to do with them ([#1972](https://github.com/vavallee/bindery/issues/1972)). The more history an instance had, the more certain it was to hit this.

### Fix

Upgrade to v1.30.4 or later. The migration now checks only what it changed; pre-existing orphans are logged as a warning and the instance starts:

```
WARN database carries pre-existing foreign-key violations from before this upgrade ... violations=2231 tables="book_files=600, books=200, downloads=831, editions=600"
```

### Optional cleanup

The warning is harmless — nothing is broken and there is no rush — but you can clear the orphans when convenient. Stop Bindery, back up the database, then:

```bash
docker run --rm -v bindery-config:/config ghcr.io/vavallee/bindery:latest db-check
docker run --rm -v bindery-config:/config ghcr.io/vavallee/bindery:latest db-repair --yes
```

Bare-metal: `bindery db-check` / `bindery db-repair --yes` (add the database path as an argument if it isn't at `BINDERY_DB_PATH`).

`db-check` lists every affected row (table, rowid, missing parent) and changes nothing. `db-repair` replays the delete rule the schema declares — `ON DELETE CASCADE` orphans are removed, `ON DELETE SET NULL` references are cleared so download history keeps its rows — and prints exactly what it did. It refuses to run without `--yes`.

If your platform can't run a different command, set `BINDERY_DB_FK_CHECK=report` (or `=repair`), start the container once, read the log, and unset it.

## A grabbed book never imports

The book shows **grabbed** in History and reaches 100% in your download client, but Bindery never imports it. Usually one of two causes.

### qBittorrent 5.x with an old Bindery

qBittorrent 5.x changed the reply it sends when a torrent is added: 4.x answered with the plain text `Ok.`, 5.x answers with a JSON object. Bindery **before 1.12.1** only understood `Ok.`, so it treats every add to a qBittorrent 5.x client as failed even though the torrent was accepted and downloads fine.

You may see `add torrent failed: {"added_torrent_ids":...}` or `failed to send to downloader`. Because Bindery believes the hand-off failed, it never records the download, so it is never imported and no `importFailed` event appears.

**Fix:** upgrade to Bindery **1.12.1 or later** (the current release is recommended). Bindery 1.11.0 and earlier cannot complete torrent grabs against qBittorrent 5.x at all — it is a hard incompatibility. After upgrading, re-grab anything that was stuck.

### Bindery cannot read the completed files

If Bindery and the download client see the storage at different paths (different container mounts), Bindery cannot find the finished download. This usually surfaces as `importFailed` in the Queue.

**Fix:** set a download-client path remap in **Settings → Download clients**, then use **Queue → Retry import**. See [Path remapping](./DEPLOYMENT.md#path-remapping-multi-container--multi-pod-setups) in `DEPLOYMENT.md`.

### "Could not match any book to this download"

The files downloaded fine, but Bindery couldn't tie them to a book in your library, so the item sits in the Queue as `importFailed` with *could not match any book to this download*. This happens when a release was grabbed without a specific book (e.g. from the free-text Search page) or its title didn't parse to a catalogue book.

**Fix:** on the failed Queue item, click **Match to book**, search your library for the correct book, and select it — Bindery imports the already-downloaded files against it and the item flips to **Imported**. If the book isn't in your library yet, add it first (Authors → the author → the book, or Add Book), then match. Once matched, an item shows **Matched to *&lt;book&gt;*** and its **Retry import** button re-runs the import against that book.

If the item was left unmatched long enough for the scanner to retry it a few times, it turns into `importBlocked` with *import retry limit reached*. That's the same situation — the files are still there — so **Match to book** and **Retry import** work exactly the same on a blocked item; matching it re-imports the recorded files, and Retry import re-arms the scanner with a fresh retry budget.

### qBittorrent files land in the download root instead of the category folder

The torrent shows the right **category** label in qBittorrent, but the files are written to the download root (e.g. `/data/downloads`) instead of the category's configured save path (e.g. `/data/downloads/torrents/audiobooks`). The poller can't find them there and the import never starts.

This happened on Bindery **1.22.1 and earlier**: Bindery sent the category **and** an explicit save path with automatic torrent management (auto_tmm) off. With auto_tmm off, an explicit save path overrides the category's save path, so qBittorrent dropped the files in the root.

**Fix:** upgrade to the current release. Bindery now enables auto_tmm and omits the explicit save path whenever a category is set, so qBittorrent places files at the category's configured save path (the source of truth for Bindery's health checks). On an older version, work around it by enabling **Automatic Torrent Management** for the category in qBittorrent, or by setting the category's save path to match Bindery's download root.

## Grab fails with "not allowed to download NZBs" (newznab error 203) on a Prowlarr-synced indexer

Searching works and the same release downloads fine from inside Prowlarr, but grabbing it in Bindery fails with something like:

```
fetch nzb: indexer refused the download (HTTP 400, newznab error 203:
This application is not allowed to download NZBs from NZBFinder.)
```

Some indexers (NZBFinder is the known case) restrict API access to a whitelist of approved applications, keyed on the client's identity rather than your API key. Prowlarr is on that list; Bindery is not yet. Prowlarr answers Bindery's grab with a redirect straight to the indexer (its per-indexer **Redirect** setting), so the indexer sees Bindery's own identity and rejects the download with error 203. The error message names both hosts when this hand-off happened.

There is **no user-side workaround**:

- Disabling Redirect in Prowlarr is not possible for Usenet indexers — Prowlarr requires it and no longer proxies NZB downloads itself (earlier versions of this page and of Bindery's error text suggested that setting; that advice was wrong, see #1424).
- Adding the indexer to Bindery directly doesn't help either: the whitelist covers the whole newznab API, so searches fail with the same error 203 even with a valid API key (#1404).
- Bindery always identifies itself honestly as `bindery/<version>` and will not impersonate Prowlarr or an arr to get around a whitelist.

**Fix:** the indexer has to add Bindery to its approved applications. For NZBFinder that request is underway (#1425 tracks it) — if you're a member there, asking them too genuinely helps. For other whitelisting indexers, point them at Bindery's stable User-Agent (`bindery/<version>`) and the request pattern (standard newznab caps/search/download on the user's own API key, same as Readarr).

## "Could not reach the metadata provider" / OpenLibrary timeout

```
metadata provider unavailable: search authors: Get "https://openlibrary.org/..."
context deadline exceeded (Client.Timeout exceeded while awaiting headers)
```

Bindery waited 15 seconds for OpenLibrary (run by the Internet Archive) and got nothing back. Common causes:

- **VPN or datacenter IP** — the Internet Archive throttles or blocks many shared VPN and hosting IP ranges.
- **OpenLibrary outage** — the Internet Archive has intermittent downtime.

Bindery's primary metadata provider is OpenLibrary or DNB (the German national library). There is currently no English alternative as the primary provider, so the fix is to make OpenLibrary reachable rather than to switch provider.

**Fixes:**

- Behind a VPN: split-tunnel `openlibrary.org` out of the VPN. Metadata lookups do not need VPN protection — only torrent traffic does — so a paid dedicated IP is not required. Switching to a different VPN exit location also often helps, since some exit IPs are blocked and others are not.
- Not on a VPN: retry later, and check the status of `openlibrary.org` / `archive.org`.

## A book is on hardcover.app but doesn't show up in Add Book / Add Author search

The provider dropdown in `Settings → General` only offers **OpenLibrary** or **DNB** for the *primary* provider. Hardcover cannot be set as the primary, and it does not need to be: it always runs as a **search enricher**. Add Book and Add Author fan the query out to the primary provider **plus** Hardcover (and Google Books, if an API key is set), then merge in any titles the primary didn't return. Books that only exist on hardcover.app are exactly what that path is meant to surface.

The catch is that **Hardcover's GraphQL API requires an API token for every query, including search** — an unauthenticated request returns `{"error":"Unable to verify token"}`. Bindery skips a provider that errors rather than failing the whole search, so without a token Hardcover contributes nothing silently and you only see OpenLibrary / DNB results.

**Fix:** add a Hardcover API token in `Settings → General` (the same token used for [Enhanced Hardcover Series](./Hardcover-Series-Wiki.md) and wishlist features), then re-run the search. Hardcover-only titles should appear in the merged results.

If results still don't appear with a token saved, confirm the instance has outbound HTTPS access to `api.hardcover.app` and that the token is valid (a bad token produces the same "Unable to verify token" error, which is logged and skipped).

## Why is the metadata button on some authors but not others?

The metadata button on an author's page only appears when Bindery thinks the author's record could be improved, so you'll see it on some authors and not others. Two cases show it:

- **"Link metadata"** — the author isn't linked to a metadata provider yet, or was created from an **Audiobookshelf / Calibre import** (those use `abs:` / `calibre:` foreign IDs). The button lets you attach a real provider record.
- **"Find better metadata"** — the author *is* linked, but the stored record is **sparse**: no description, no image, no disambiguation, and no ratings. The button searches the providers for a richer match to relink to.

An author that already has a filled-in record (a description, an image, ratings) hides the button, because there's nothing obviously better to fetch. So a missing button means that author already has good metadata. If an author looks well populated but still shows the button, the stored description/image/ratings are likely empty even though the page renders other fields — relink and pick the best match to fill them in.

## An author has far fewer books than they should after a refresh

The catalogue sync filters the works the metadata provider returns before creating book rows, so a refresh can legitimately end with far fewer books than the author has written. After the refresh finishes, the author's page shows a note above the book list saying how many works were skipped and by which filter — reload the page if the refresh was still running when you last looked.

The usual culprit is the **allowed languages** list on the author's metadata profile (`Settings → Metadata`). Two halves of that setting drop books:

- **The language list itself.** A work whose language is outside the list is skipped. Foreign-language editions of an English author are the common case.
- **"When book language is unknown".** OpenLibrary carries no language on many *work* records, so a large tail of an author's catalogue arrives with no language at all. Set to **fail**, every one of those is skipped too — which is what turns "a few translations were dropped" into "most of this author is missing". Setting it to **pass** and refreshing again brings them back.

The skip counts are also in the log (`Settings → Logs`): the `author books synced` line carries `added`, `skipped_language`, `skipped_junk` and `skipped_media_type`, and is logged at WARN whenever anything was skipped. Per-book detail (which title, which language) is at DEBUG.
