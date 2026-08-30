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

If Bindery and the download client see the storage at different paths (different container mounts), Bindery cannot find the finished download. This usually surfaces as `importFailed` in the Queue with *nothing at `<path>` on this host*.

That message lists three causes, because all three produce the same missing path and only you can tell them apart:

1. **The download hasn't finished.** With qBittorrent's temp/incomplete directory enabled (`Session\TempPathEnabled=true`), the final save path doesn't exist until qBittorrent finishes and moves the payload there. Nothing to do — Bindery imports it when it lands.
2. **The files moved or were deleted since.** Common after deleting a book (or its files) from the UI while the torrent stays in the client, and after an import in `move` mode. Re-download, or point the client back at the files.
3. **Bindery and the client see different filesystem roots.** This is the path-remap case: set a download-client path remap in **Settings → Download clients**, then use **Queue → Retry import**. See [Path remapping](./DEPLOYMENT.md#path-remapping-multi-container--multi-pod-setups) in `DEPLOYMENT.md`.

**Download client on Windows, Bindery on Linux or Docker?** The client reports a drive path like `S:\Downloads`, which can never exist inside Bindery, so a remap is mandatory. Write the pair with the drive letter on the left: `S:\Downloads:/mnt/Storage/Downloads`. The colon after the drive letter is part of the path, not a second separator. Bindery **1.32.1 and earlier** read that same string as `S` mapping to `\Downloads:/mnt/Storage/Downloads`, so the remap silently did nothing: torrents were added, author and book folders were created, and no file was ever copied. If the Test button warns about the path on one of those versions, upgrade; no spelling of the pair works there. See [Windows download client, Bindery on Linux](./DEPLOYMENT.md#windows-download-client-bindery-on-linux-or-docker) in `DEPLOYMENT.md`.

Bindery does not spend import retry attempts while there is nothing at the path, so a download in this state keeps its full retry budget for attempts that could actually work — it stays `importFailed` with the reason visible, and imports on its own as soon as the files appear.

If the files never appear, it does not wait forever: after about 30 minutes of finding nothing (120 poll cycles) the download flips to `importBlocked` with a message naming the path it checked. From there you can **Retry import** once you've fixed the path, or grab the release again from search — a blocked entry no longer blocks a re-grab.

### "Already grabbed" when re-grabbing a release

Clicking **Grab** on a release you already have a Queue entry for is refused with *already grabbed*, and the message now names the state that entry is in.

- **`importFailed`** — the scanner is still working on that download. Use **Queue → Retry import** to re-run the import against the files it already has, or remove the Queue entry if you want to grab the release fresh. If its files are simply not there, it turns into `importBlocked` (see above) and becomes re-grabbable on its own.
- **`imported`** — you already have it.
- **downloading / grabbed / importing** — it's in flight; check the Queue.
- **`importBlocked`** — a re-grab is allowed and reuses the existing Queue row with a fresh retry budget. Use this when the original files are gone; use **Retry import** instead when they're still on disk.

### "Could not match any book to this download"

The files downloaded fine, but Bindery couldn't tie them to a book in your library, so the item sits in the Queue as `importFailed` with *could not match any book to this download*. This happens when a release was grabbed without a specific book (e.g. from the free-text Search page) or its title didn't parse to a catalogue book.

**Fix:** on the failed Queue item, click **Match to book**, search your library for the correct book, and select it — Bindery imports the already-downloaded files against it and the item flips to **Imported**. If the book isn't in your library yet, add it first with **Books → Add Book** (or through its author), then match. Once matched, an item shows **Matched to *&lt;book&gt;*** and its **Retry import** button re-runs the import against that book.

If the item was left unmatched long enough for the scanner to retry it a few times, it turns into `importBlocked` with *import retry limit reached*. That's the same situation — the files are still there — so **Match to book** and **Retry import** work exactly the same on a blocked item; matching it re-imports the recorded files, and Retry import re-arms the scanner with a fresh retry budget.

### qBittorrent files land in the download root instead of the category folder

The torrent shows the right **category** label in qBittorrent, but the files are written to the download root (e.g. `/data/downloads`) instead of the category's configured save path (e.g. `/data/downloads/torrents/audiobooks`). The poller can't find them there and the import never starts.

This happened on Bindery **1.22.1 and earlier**: Bindery sent the category **and** an explicit save path with automatic torrent management (auto_tmm) off. With auto_tmm off, an explicit save path overrides the category's save path, so qBittorrent dropped the files in the root.

**Fix:** upgrade to the current release. Bindery now enables auto_tmm and omits the explicit save path whenever a category is set, so qBittorrent places files at the category's configured save path (the source of truth for Bindery's health checks). On an older version, work around it by enabling **Automatic Torrent Management** for the category in qBittorrent, or by setting the category's save path to match Bindery's download root.

## A download client tests fine but every poll logs "invalid character '<'"

```
qBittorrent category path check failed: decode categories: invalid character '<' looking for beginning of value
```

The client answered with a web page where Bindery expected JSON. Almost always the **Host** field holds more than a hostname, so every API request lands on the client's own web UI index page instead of its API. That page is served with a perfectly healthy HTTP 200, which is why **Test** used to report the connection verified: something did answer.

The value behind the message above was `192.168.1.50:8080/#/`, copied straight out of a browser address bar. Bindery appended its own port and path to it, so requests went to `http://192.168.1.50:8080/#/:8080/api/v2/...`, and the `#` threw away everything after it (#2203).

**Fix:** open **Settings → Download clients**, edit the client, and leave a bare hostname or IP in Host. The port belongs in **Port**, a reverse proxy prefix belongs in **URL Base**, and `https` is the **Use SSL** checkbox. See [What goes in a download client's Host field](./DEPLOYMENT.md#what-goes-in-a-download-clients-host-field) for the full table.

Current releases refuse to save a Host like that, fail **Test** with a message naming the port and path to move, and turn the client's health dot red, so a client carried over from an older version reports the problem the moment you press Test rather than at the next poll.

If Host is already a bare hostname and the message persists, something in front of the client is serving its own page: check that **URL Base** matches the path your reverse proxy serves the client on.

## The job stays in the download client after Bindery imports it

The book imports fine and shows **Imported** in the Queue, but the job is still sitting in SABnzbd's history (or NZBGet's, or still listed in your torrent client). Once Bindery has taken ownership of the files it asks the client to forget the job, and the client is allowed to say no.

Bindery logs that refusal. Look for a `cleanup failed` warning naming the client:

```text
level=WARN msg="cleanup failed" error="SABnzbd rejected history delete: Job not found" clientType=sabnzbd remoteID=SABnzbd_nzo_abc123
```

Versions before this fix (#2192) produced no log line at all, because SABnzbd and NZBGet report a refused action as a normal HTTP 200 response with a failure flag inside it, and Bindery read only the HTTP status. Nothing about the client's behaviour changed, only whether Bindery notices it.

Common reasons a client refuses:

* **The job was already removed**, by hand or by the client's own retention setting. Harmless, and the message usually says so ("Job not found", or an unknown ID).
* **The client restarted between the download finishing and the import**, losing the history slot.
* **The API user has no permission** to modify history or the queue. Check the API key or the RPC username's rights.
* **The client is busy repairing or unpacking** something else and declines the edit. It normally succeeds on the next import.

Nothing else is affected. The import completed and the book is in your library either way, and removing a Queue item in Bindery still removes Bindery's own row even when the client refuses to drop its copy. If the warning repeats for every import, the job is genuinely piling up on the client side and the reason in the message is what to fix.

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

## Grab fails with "SABnzbd rejected download" and the book goes back to Wanted

The grab fails immediately and the book returns to **Wanted**. On Bindery **1.32.0 and earlier** the error named the download client:

```
SABnzbd rejected download
```

while SABnzbd's own log — on the far side of the integration — said the file it was handed wasn't XML at all:

```
Invalid NZB file <release>.nzb, skipping (error: syntax error: line 4, column 0)
```

SABnzbd is right and it is not the culprit. Bindery fetches the NZB from the indexer itself and hands the download client the bytes, so what the client rejected is a response the **indexer** got wrong. Indexers land here by answering a refused, expired or rate-limited grab with an error page under HTTP 200 instead of a 4xx — a 4xx would have been reported against the indexer all along.

The same shape reaches NZBGet; both usenet clients share the fetch path.

**Fix:** upgrade to the current release. Bindery now checks that the fetched body is an NZB before the download client sees it, and reports what actually arrived:

```
fetch nzb: the indexer returned HTTP 200 with a body that is not an NZB:
You have reached your download limit for today.
```

Then act on the snippet — a spent download quota or an expired grab token is the common case, so wait it out or grab from another indexer; an HTML login or challenge page means the indexer session or API key needs attention. On an older release there is nothing to read: SABnzbd purges an invalid NZB before its own backup step, so the bytes are gone by the time you look.

## Deluge's connection test passes but every grab fails

**Test connection** in Settings → Download Clients reports success, and
Deluge's own log shows Bindery's request arriving at the Web UI JSON API:

```
json-request: {"method": "core.add_torrent_magnet", ...}
```

but no torrent is ever added, and the book goes back to **Wanted**. The same
magnet added by hand in the Deluge Web UI works fine.

Deluge runs as two processes. `deluge-web` serves the Web UI and its JSON API;
`deluged` is the daemon that holds the torrents. Logging in authenticates
against `deluge-web` alone, and a Web UI session has to be attached to a daemon
before the Web UI has anywhere to send a `core.` method. Where `deluge-web`
auto connects to a single local daemon on startup this step is invisible. Where
it does not, or where the **Connection Manager** holds more than one host, login
keeps succeeding while every `core.` call fails, which is exactly the pairing of
a passing test and failing grabs (#2204).

**Fix:** upgrade. Bindery now attaches the session to a daemon after logging in,
and the connection test fails with the reason when it cannot, rather than
reporting success.

Two cases still need you, and the error message names which one you are in:

- **No daemon host configured.** Open the Deluge Web UI, add your `deluged`
  under **Connection Manager**, and connect to it.
- **More than one host configured.** Bindery will not guess which daemon you
  meant, because picking the wrong one files downloads somewhere the importer
  never looks. Connect to the one you want in the Web UI and tick its **auto
  connect** box so the session comes back attached on its own.

## Ebook searches on Prowlarr-synced indexers return zero results

Audiobook searches work, indexer tests pass, and running the same query inside Prowlarr returns releases, but every ebook search in Bindery finds nothing. Open **Settings → Indexers** and look at the synced indexer's **Categories**: if it lists `7030` (Books/Comics) and no `7020` (Books/EBook), this is the cause. Every ebook query goes out as `cat=7030`, which is where comics live, not ebooks.

Prowlarr's indexer API does not report a per-indexer category list, so Bindery derives one from the **Sync Categories** of the applications registered in Prowlarr. In 1.32.1 and earlier, any application whose sync categories fell in the Newznab book (7xxx) or audio (3xxx) ranges counted, which meant **Mylar** contributed its comics category `7030` and **Lidarr** contributed the music 3xxx range. A Mylar-shaped scope is the damaging one: it leaves the ebook category list non-empty but wrong, which suppresses both the fallback to the indexer's own capabilities and the search-time `7020` default.

**Fix:** upgrade. Bindery now takes application scopes only from Readarr and LazyLibrarian, the two Prowlarr applications that actually sync books. With neither registered, the indexer's own advertised categories are used, which is what standalone Prowlarr users already got. Bindery also logs a WARN at sync time when an indexer advertises an ebook category that no registered application syncs.

On an older version, the workaround is the per-indexer **Include parent categories** toggle, which widens the ebook query to `7000,7030` and does return the 7020 releases at the cost of a broader audiobook query. Editing the indexer's categories by hand does not survive the next sync, which rewrites them.

## "Could not reach the metadata provider" / OpenLibrary timeout

```
metadata provider unavailable: search authors: Get "https://openlibrary.org/..."
context deadline exceeded (Client.Timeout exceeded while awaiting headers)
```

Bindery waited 15 seconds for OpenLibrary (run by the Internet Archive) and got nothing back. Common causes:

- **VPN or datacenter IP** — the Internet Archive throttles or blocks many shared VPN and hosting IP ranges.
- **OpenLibrary outage** — the Internet Archive has intermittent downtime.

Bindery's primary metadata provider is OpenLibrary, DNB (the German national library), or Hardcover. If OpenLibrary is unreachable for you long-term, switching the primary provider to Hardcover (`Settings → Metadata Profiles → Library Defaults`, requires a Hardcover API token) is a working English-language alternative; otherwise the fix is to make OpenLibrary reachable.

**Fixes:**

- Behind a VPN: split-tunnel `openlibrary.org` out of the VPN. Metadata lookups do not need VPN protection — only torrent traffic does — so a paid dedicated IP is not required. Switching to a different VPN exit location also often helps, since some exit IPs are blocked and others are not.
- Not on a VPN: retry later, and check the status of `openlibrary.org` / `archive.org`.

## A book is on hardcover.app but doesn't show up in Add Book / Add Author search

Hardcover does not have to be the primary provider for its titles to show up: it always runs as a **search enricher** too. Add Book and Add Author fan the query out to the primary provider **plus** Hardcover (and Google Books, if an API key is set), then merge in any titles the primary didn't return. Books that only exist on hardcover.app are exactly what that path is meant to surface.

The catch is that **Hardcover's GraphQL API requires an API token for every query, including search** — an unauthenticated request returns `{"error":"Unable to verify token"}`. With no token saved, Bindery skips Hardcover before sending anything, so it contributes nothing silently and you only see OpenLibrary / DNB results. Startup says so too: the log reads `hardcover enrichment idle: no api token configured` instead of `hardcover enrichment enabled`. Saving a token takes effect on the next lookup, with no restart.

**Fix:** add a Hardcover API token in `Settings → General` (the same token used for [Enhanced Hardcover Series](./Hardcover-Series-Wiki.md) and wishlist features), then re-run the search. Hardcover-only titles should appear in the merged results.

If you want Hardcover to *define* author catalogues rather than only add to them, set it as the primary provider in `Settings → Metadata Profiles → Library Defaults` and restart. The option stays disabled until a token is saved, and clearing the token is refused while Hardcover is primary — a tokenless Hardcover primary would fail every lookup. If the token is removed out-of-band (direct DB edit, restored backup), Bindery logs a warning at startup and falls back to OpenLibrary rather than booting with dead metadata.

If results still don't appear with a token saved, confirm the instance has outbound HTTPS access to `api.hardcover.app` and that the token is valid (a bad token produces the same "Unable to verify token" error, which is logged and skipped).

## Hardcover fails with HTTP 500, or the token test shows a Hardcover error page

```
hardcover search books: HTTP 500 (upstream returned a non-JSON response, likely an error page,
so this is a Hardcover-side failure rather than a token problem)
```

This means `api.hardcover.app` answered with something that wasn't JSON, almost always its own HTML error page. It's an outage on Hardcover's side: no token change, reinstall or setting fixes it. Wait for Hardcover to recover, then press **Test** again. Bindery deliberately doesn't print the page it received, because pasting Hardcover's markup into the Settings UI made an upstream outage read like a bad token (#2128).

The error tells you which side failed:

| Message | Meaning | What to do |
|---|---|---|
| `token rejected (HTTP 401: ...)` | Hardcover read the token and refused it | Reissue the token at [Hardcover API settings](https://hardcover.app/account/api) and save it again |
| `HTTP 400: ...` / `HTTP 403: ...` plus text from Hardcover | Hardcover rejected the request and said why | Act on the quoted reason; the token itself is fine |
| `HTTP 5xx (upstream returned a non-JSON response, likely an error page, ...)` | Hardcover served an error page | Nothing on your side. Retry later |
| `HTTP 5xx (upstream returned an empty response body, ...)` | Nothing came back at all | Usually Hardcover, occasionally a proxy in front of your instance. Check `BINDERY_OUTBOUND_PROXY` if you set one |
| `GraphQL: ...` | Hardcover answered normally but rejected the query | A Bindery bug. Please file an issue with the message |

**On token formats:** Hardcover issues `hc_pat_` personal access tokens alongside the older JWTs, and **both work**. Bindery sends whatever you save as `Authorization: Bearer <token>`, which is the scheme Hardcover accepts for either format, so there is nothing to configure per format. Pasting the whole header is fine too: a leading `Bearer ` or `Authorization: ` is stripped before the token is stored. Verified against the live API on 2026-08-24, a made up `hc_pat_...` value comes back as `401 invalid_token`, so a PAT that fails with 401 is a token to reissue, while a PAT that fails with 500 is an outage to wait out.

## Why is the metadata button on some authors but not others?

The metadata button on an author's page only appears when Bindery thinks the author's record could be improved, so you'll see it on some authors and not others. Two cases show it:

- **"Link metadata"** — the author isn't linked to a metadata provider yet, or was created from an **Audiobookshelf / Calibre import** (those use `abs:` / `calibre:` foreign IDs). The button lets you attach a real provider record.
- **"Find better metadata"** — the author *is* linked, but the stored record is **sparse**: no description, no image, no disambiguation, and no ratings. The button searches the providers for a richer match to relink to.

An author that already has a filled-in record (a description, an image, ratings) hides the button, because there's nothing obviously better to fetch. So a missing button means that author already has good metadata. If an author looks well populated but still shows the button, the stored description/image/ratings are likely empty even though the page renders other fields — relink and pick the best match to fill them in.

## Books are filed under the wrong author after a Calibre import

Symptom: a Calibre library imports, and an author you own dozens of books by
has no author page at all. Their titles are on somebody else's page, usually a
co-author or a joint pen name. The log shows lines like:

```
DEBUG calibre import: alias record skipped error="alias \"Isaac Asimov\" already points at author 1125 (refusing to reassign to 105)" name="Isaac Asimov"
```

The scan then reports those same names under **Unmatched files** with "Parsed
author isn't in your library", because the name only exists as an alias.

What happened: for a book credited to several people, the import used to record
every co-author as an *alias* of the first credited author ([#1684](https://github.com/vavallee/bindery/issues/1684)).
An alias means "another name for this same person", so from then on every book
by that co-author resolved back to their collaborator, and they never got an
author row of their own. Which author survived came down to Calibre book id
order, which is why it looked arbitrary. Rolling the import back does not undo
it: aliases are shared entities, so a rollback leaves them in place.

### Fix

Upgrade, then re-import. Co-authors are no longer recorded as aliases, and an
import now ignores an existing alias unless something backs it up, so the real
authors get created on the next run. Nothing is deleted for you, because the
database cannot tell a co-author alias apart from a legitimate one.

To tidy up the leftovers, open the author page the books were wrongly filed
under, find the wrong names in the **Aliases** list, and use the remove button
on each. Books already attached to the wrong author stay there: move them with
**Merge authors** on the Authors page, or edit the book's author directly.

An alias is still honoured when it carries real provenance (an author merge, or
a provider record), when it is simply a spelling variant of the author's own
name, or when it is a latin-script name on a non-latin author (the "Murakami"
to "村上春樹" case). Those keep working exactly as before.

## An author has far fewer books than they should after a refresh

The catalogue sync filters the works the metadata provider returns before creating book rows, so a refresh can legitimately end with far fewer books than the author has written. After the refresh finishes, the author's page shows a note above the book list saying how many works were skipped and by which filter — reload the page if the refresh was still running when you last looked.

The usual culprit is the **allowed languages** list on the author's metadata profile (`Settings → Metadata`). Two halves of that setting drop books:

- **The language list itself.** A work whose language is outside the list is skipped. Foreign-language editions of an English author are the common case.
- **"When book language is unknown".** OpenLibrary carries no language on many *work* records, so a large tail of an author's catalogue arrives with no language at all. Set to **fail**, every one of those is skipped too — which is what turns "a few translations were dropped" into "most of this author is missing". Setting it to **pass** and refreshing again brings them back.

Two more settings on the same profile drop books by looking at the work's **editions** rather than the work itself:

- **Min pages.** A work is skipped when no edition of it reports a page count at or above the floor. A work whose editions carry no page count at all is treated as unknown and kept, not skipped.
- **Skip missing ISBN.** A work is skipped when no edition of it carries an ISBN. A work the provider returns no editions for counts as missing and is skipped too.

Both read the full edition list. Older versions fetched only the first 50 editions of an OpenLibrary work, in an order OpenLibrary does not sort, so a heavily reprinted title whose ISBN or page count happened to sit further down the list was skipped even though it qualified. If either setting is on and books went missing that way, refresh the author again after upgrading.

The skip counts are also in the log (`Settings → Logs`): the `author books synced` line carries `added`, `skipped_language`, `skipped_junk` and `skipped_media_type`, and is logged at WARN whenever anything was skipped. Per-book detail (which title, which language) is at DEBUG.

## A book shows a file path that no longer exists

Symptom: you moved or renamed a book's file (or the folder holding it), scanned, and the book still shows the old path. It keeps saying **In Library**, download and delete act on a file that isn't there, and rescanning changes nothing.

What happens: Bindery tracks files as rows, and registering a file at a new location *adds* a row rather than replacing the old one, so the book ends up owning both. Older versions always rendered the oldest row, so once the old path was dead it stayed on screen ([#2186](https://github.com/vavallee/bindery/issues/2186)).

Fixed (#2186): a book now renders whichever of its tracked files still exists on disk, and a **Scan Library** run also repairs books that were already stuck this way, so on a current version the fix is to scan once. The row for the old location is deliberately *not* deleted. To clear it, open the book, find the dead path in the **Files** list, and use **Forget this file**, which drops the tracking row only and never touches the filesystem.

Two things this does not do:

- It does not find the file for you. Bindery only knows about the new location once a scan (or an import) has registered it, and a scan only matches files to books already in your catalogue (see rule 1 in the [User Guide](User-Guide-Wiki.md#1-library-scan-is-a-reconciler-not-an-importer)). A file moved *outside* your library root is not found at all.
- It does not react to a storage outage. If a mount is temporarily unavailable then every path under it looks missing at once, so Bindery deliberately keeps showing what it showed before rather than acting on the absence. Nothing is deleted or reset, and the books come back as they were when the mount does.

If you reshape your library regularly, **Rename files** on the book or author page is the supported way to do it: it moves the file *and* repoints the same tracking row at the new location, so there is never a second row to clean up.

## Collecting logs for a bug report

`Settings → Logs` is the whole log store, so you don't need shell access to the container (rootless images give you nowhere to `cat` a file anyway).

Filter down to the problem first — level, component, a search term, and a date range around when it happened — then click **Download** next to *Clear filters*. You get a plain-text file named `bindery-logs-<timestamp>.txt` containing exactly the entries the table was showing, with a header block recording which filters produced it. Attach that to the issue.

Notes:

- One entry per line, `timestamp LEVEL [component] message key=value`, so it stays greppable and pastes cleanly into an issue.
- API keys and tokens that appear in logged URLs are replaced with `REDACTED` on the way out. Still skim the file before posting — paths and book titles are not redacted.
- An export stops at 50,000 entries and says so in the last line of the file. If you hit that, narrow the level or the date range.
- Admin-only, like the rest of the Logs tab.
- Turn the **Runtime level** up to `DEBUG` before reproducing if the default output isn't enough; entries are persisted, so the download picks them up after the fact.
