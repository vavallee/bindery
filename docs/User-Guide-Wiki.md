# User guide: how Bindery thinks

This page explains the flow of the app — the mental model behind the buttons.
It exists because most questions in Discord and GitHub trace back to a handful
of concepts that Bindery never states outright. If you are setting up a fresh
install, do [QUICKSTART.md](QUICKSTART.md) first; this page explains *why*
those steps look the way they do and what happens after them.

---

## The one idea that explains everything: catalogue first, files second

Bindery is **metadata-first**. The flow is:

```
metadata providers                indexers              download client
(OpenLibrary, Hardcover, …)   (Newznab / Torznab)    (qBittorrent, SAB, …)
        │                             │                       │
        ▼                             ▼                       ▼
  add an author  ──►  catalogue  ──►  Wanted  ──►  grab  ──►  download
                     (book records)  (search targets)             │
                                                                  ▼
                                                               import
                                                     (hardlink/copy into library,
                                                      rename per your template)
                                                                  │
                                                                  ▼
                                              library ──► ABS / Calibre / OPDS / Grimmory
```

You tell Bindery *what books exist* (by adding authors, importing a library,
or syncing a list). Book **records** come from metadata providers. Files only
ever **attach to records that already exist**. Bindery then works to fill the
records it is told to pursue.

If you are coming from Sonarr or Radarr you may expect the opposite,
**release-first** flow: search your tracker, pick a release, and have the
software identify and file it. Bindery does not work that way, because books
have no TVDB equivalent and no scene naming convention — reverse-identifying
an arbitrary release is unreliable. The escape hatches:

- **Search Indexers** (the magnifier icon in the header) — free-text search
  across all your indexers; any result can be grabbed.
- **Manual Import** (`/import`) — point it at a folder of files you already
  have and match them to books.

Both still end by attaching a file to a catalogue record.

## Five rules that answer most questions

Almost every "why is Bindery doing that?" question comes down to one of these.

### 1. Library Scan is a reconciler, not an importer

**Scan Library** (Settings → General → Library, and automatically every 6
hours) matches files already on disk to books **already in your catalogue**.
It never creates authors or books from files. If you point a fresh install at
a folder of 3,000 epubs and hit Scan, you get 3,000 "unmatched" files and an
empty library — nothing populated the catalogue first.

The sequence that works is always: **populate the catalogue, then scan**. See
[Bringing in an existing library](#bringing-in-an-existing-library) below.

### 2. Status and Monitored are two different switches

Every book has a **status** — its acquisition lifecycle:

`wanted → downloading → downloaded → imported` (plus `skipped`)

Every new book record starts as `wanted`, no matter how it was created. That
is just "no file yet"; it does not by itself make Bindery do anything.

Separately, every book is **monitored** or not. Monitored means "Bindery
actively pursues this." The **Wanted page** — and the automatic search sweep —
only covers books that are *all three of*: status `wanted`, monitored, and not
excluded. A book that is `wanted` but unmonitored shows as "Not monitored" and
is left alone.

Two related labels:

- **In Library** = status `imported` = Bindery can see the file on disk.
- **Exclude vs Delete**: deleting an unwanted catalogue book is temporary —
  the next metadata refresh recreates it. **Exclude** is the sticky action; it
  hides the book and keeps it out of searches permanently.

### 3. Bindery never watches folders

Imports are driven by the download clients, not by the filesystem. Every 15
seconds Bindery asks each client over its API which jobs finished and where
the files landed, and imports from *the path the client reports*.
Consequences:

- `BINDERY_DOWNLOAD_DIR` is **not a watch folder**. Completed downloads do not
  need to be consolidated there, and dropping a file into it does nothing.
  The variable is used for validation, the storage health check, and as the
  save path Bindery hands qBittorrent.
- There is no per-protocol (torrent vs usenet) folder setting because each
  client already owns its completed path.
- Files you acquired outside Bindery are picked up only via **Manual Import**,
  **Bulk folder import**, or a **Library Scan** (rule 1 applies).

### 4. Naming templates are for output, not input

The templates in Settings → General → File Naming control how Bindery names
and organises files **it imports itself**. The Library Scan does **not** use
them — it reads existing files with a fixed parser that prefers an
`{Author}/{Book Title}/` folder structure and reads a bare `X - Y` filename as
`Title - Author` (the *opposite* of Readarr's default order). If the scan
misreads your `Author - Title.epub` files, that is why: rearrange into
author folders, or use Manual Import, which lets you pick the right book.

### 5. Hardlinks need one mount

The default import mode (`auto`) hardlinks a completed torrent into the
library — instant, no extra disk, seeding keeps working — but **only when
downloads and library are on the same filesystem**. Two Docker bind mounts
(`/downloads` + `/books`) are two filesystems, and `auto` silently falls back
to copying. Unraid `/mnt/user` paths and separate ZFS datasets have the same
effect even when they look like one tree. Mount a single parent
(`/data/downloads` + `/data/media`) if you want hardlinks. Full layout and
the import-mode table:
[Storage & hardlinks](Storage-And-Hardlinks-Wiki.md). Never use `move` mode
if you seed.

---

## Adding books

All roads create catalogue records; they differ in how many and what gets
monitored.

| Entry point | What it creates |
|---|---|
| **Authors → Add Author** | The author **plus their full catalogue** (up to ~100 titles), monitored per the monitor mode you pick |
| **Authors → Add Book** (title/ISBN/ASIN) | One book, silently creating its author if needed |
| **Discover → Add to Wanted** | One recommended book |
| **Series → Fill gaps** | The missing books of a linked series, wanted + monitored |
| **Import lists** (Settings → Import, Hardcover reading lists) | Every list item, re-synced daily |
| **Library imports** (Calibre, Readarr, ABS, Goodreads CSV, author list) | Your existing catalogue — see the next section |

Two settings decide whether an author add stays a trickle or becomes a flood:

- **Monitor mode** (per author): *All books* (default), *Future books only*,
  *Latest only*, *None*, or *By series*. With the default, adding a prolific
  author monitors their entire back-catalogue, and every one of those books is
  a search target. If you only want new releases, pick *Future books only*;
  the global default lives in Settings → Metadata Profiles → Library Defaults
  → **Default monitor mode**.
- **Monitor new items** (per author): whether books *discovered by later
  metadata refreshes* follow the monitor mode or arrive unmonitored.

The Books page shows the **whole catalogue** — monitored or not. "Why are
there books here I never asked for?" is rule 2: unmonitored means "won't
grab", not "won't list". Select the ones you never want and **Exclude** them.

## From Wanted to your library

**Search.** A scheduled sweep (default every 12 hours; interval in Settings →
General, restart required) searches your indexers for every book on the
Wanted page and auto-grabs the best release. The **Auto-grab** toggle in
Settings → General turns the sweep's grabbing off entirely if you prefer to
grab by hand from the Wanted page. Searches also fire when an author is added
("Search for books on add") and when a book flips to wanted.

**Decision.** Each release is checked against your quality profile (allowed
formats, cutoff), delay profile, blocklist, size limits, and language filter.
On indexers marked *freeleech only*, non-freeleech releases are not discarded
— they are parked as **pending** for manual approval.

**Grab.** Torznab (torrent) indexers route to your torrent client, Newznab
(usenet) to your NZB client — which is why the client's protocol must match
the indexer, and why the category (default `books`) must already exist in the
client. Bindery fetches the .torrent/NZB itself and hands it over.

**Import.** When the client reports the job complete, Bindery matches it to
the book, places the file per your import mode and naming template, and marks
the book **In Library**. Ebooks land under the author's root folder (falling
back to the default root folder, then `BINDERY_LIBRARY_DIR`); audiobooks have
their own destination chain (`BINDERY_AUDIOBOOK_DIR`, per-author override).
After import, Bindery fans out to whatever integrations you enabled: Calibre,
a CWA ingest folder, Grimmory's BookDrop, an Audiobookshelf library scan,
webhooks.

**Queue and History.** The Queue page shows live downloads and, importantly,
the recovery actions: **Retry import** (after fixing a path remap), **Match to
book** (attach a failed import to the right book and import it from disk), and
per-row error detail. History records every grab/import/failure and can
blocklist a bad release in one click.

Bindery does not chase format upgrades on its own: the sweep only searches
Wanted books, and once a book has a file it is no longer Wanted. If you want
a specific format, search from the book's page and grab it yourself.

## Bringing in an existing library

The most common onboarding stumble. Remember rule 1: **something must create
the catalogue records before any files can attach**. Pick the row that
matches where your metadata lives, then scan:

| You have | Do this first |
|---|---|
| A Calibre library | Settings → Calibre → **Library import** (reads `metadata.db`, creates authors + books) |
| A Readarr install | Settings → Import → upload `readarr.db` ([guide](Migrating-From-Readarr-Wiki.md)) |
| An Audiobookshelf server | Settings → Audiobookshelf → configure + **Import** ([guide](ABS-Import-Wiki.md)) |
| A Goodreads account | Settings → Import → **Goodreads CSV** (export, filter by shelf, preview, commit) |
| Just a list of authors | Settings → Import → paste or upload the author list |
| Only folders of files | Use **Manual Import** (`/import`) or Settings → Import → **Bulk folder import**, which match files and create what's missing |

Then run **Settings → General → Library → Scan Library** to attach your files
to the records. Things worth knowing before you judge the results:

- Library imports create **records only** — no covers, no descriptions, no
  files. Run **Refresh metadata** on authors to fill in covers and details,
  and the scan to attach files. "Fresh import looks empty" is expected, not
  broken.
- The scan only matches files whose **author already exists** in Bindery, by
  normalised name — `B. Sanderson/` on disk won't match a "Brandon Sanderson"
  author row.
- ABS imports that "lose" titles usually didn't: ambiguous matches are parked
  in the **review queue** (Settings → Audiobookshelf) for you to resolve, and
  the import summary counts them.
- Unmatched files are listed after the scan, each with the reason it missed:
  the parsed author isn't in your library (fix the file's tags or folder name),
  the author matched but has no book waiting for a file (populate that author's
  catalogue), no title matched, or no title could be read from the file at all
  (rename it). Use **Manual Import** to resolve the rest by hand.
- A folder holding both an ebook and an audiobook for the same book attaches
  both in a single scan — one file per format, so a second scan is not needed.
- A PDF, TXT, RTF or CBZ sitting in a folder that also holds audio is treated as
  an **audiobook supplement** (the companion PDF Audible-style releases ship)
  and is not attached as the book's ebook. The same file in a folder with no
  audio in it is treated as an ebook as usual.

## Metadata: where book data comes from

- **OpenLibrary** is the default primary provider — it decides what an
  author's catalogue looks like. It is community data: expect occasional
  duplicates, language mix-ups, and box-set entries. The "primary" selector
  (Settings → Metadata Profiles → Library Defaults) offers OpenLibrary or
  **DNB** (German National Library) only.
- **Hardcover** is an *enricher*, not a primary — it improves search results,
  ratings, and series data, and powers import lists and the Discover wishlist
  row. **Without an API token (Settings → API Keys) Hardcover is silently
  skipped everywhere.** The free token is the single highest-value config for
  metadata quality.
- **Google Books** (free API key) and **Audnexus/Audible** (audiobook
  narrator, duration, by ASIN) enrich further.

When metadata is wrong, you have three levels of fix:

1. **Edit metadata** on the book — edited fields are **locked** so refreshes
   never overwrite them ([guide](Metadata-Editing-Wiki.md)).
2. **Re-bind** the book, or **relink** the author ("Find better match"), to a
   different provider record when the match itself is wrong.
3. A **metadata profile** (languages, minimum popularity, skip part-books)
   filters what a catalogue sync lets in.

A **metadata refresh** re-syncs an author's catalogue from the provider. Note
that it can discover new works — whether those arrive monitored is the
author's *Monitor new items* setting (rule 2's flood control).

## What Bindery deliberately does not do

Knowing the edges saves time:

- **Track reading.** No read/unread, progress, or ratings. Bindery acquires
  and organises; Audiobookshelf, Hardcover, or your reader do consumption.
- **Identify arbitrary releases.** No release-first flow (see the top of this
  page).
- **Watch input folders.** Rule 3. (The *drop folder* setting is the reverse:
  an output copy for a sibling tool to ingest.)
- **Create download-client categories.** Make the category in the client
  first.
- **Chase format upgrades** on its own.
- **Per-user root folders.** Multi-user tenancy scopes authors, books, and
  downloads per user; root folders stay a shared, admin-managed pool
  ([multi-user.md](multi-user.md)).

## Quick answers

**I added one author and now have 100+ wanted books.**
Monitor mode *All books* on a prolific author. Bulk-select on the author page
and Unmonitor or Exclude; set the default monitor mode to *Future books only*
in Settings → Metadata Profiles → Library Defaults before adding more.

**Scan Library sees my files but imports nothing.**
Rule 1 — the catalogue is empty or the authors don't exist yet. Populate
first ([Bringing in an existing library](#bringing-in-an-existing-library)),
then scan.

**I deleted books I don't want and they came back.**
Delete is undone by the next metadata refresh. Use **Exclude**.

**A book is on hardcover.app but doesn't show up in search.**
No Hardcover token configured — set one in Settings → API Keys.
([troubleshooting](Troubleshooting-Wiki.md))

**The torrent finished ages ago but never imported.**
Check the Queue for the error. Usual causes: category mismatch, the client
and Bindery seeing the same storage at different paths (set a **path remap**
on the download client), or files the container can't read. **Retry import**
after fixing. ([troubleshooting](Troubleshooting-Wiki.md))

**My browser can reach qBittorrent but Bindery's Test fails.**
In Docker, `localhost` inside Bindery's container is Bindery, not the client —
use the service name or LAN IP. Also check for qBittorrent's persisted IP ban
after failed logins. Note the image is distroless: there is no shell to debug
from inside the container.

**Bindery is behind my VPN and metadata broke.**
OpenLibrary blocks many VPN/datacenter IPs. Keep Prowlarr and the torrent
client behind the VPN; Bindery itself doesn't need it — it only talks to
Prowlarr, never to trackers directly. Gluetun users: allow LAN with
`FIREWALL_OUTBOUND_SUBNETS`, or ABS/Calibre connections will time out.

**The book has my ebook but still shows as not done.**
Its media type is *Both*, so it also wants the audiobook (each format has its
own lifecycle). Set the book to *Ebook* on the Books page, or set the default
media type — and optionally the "restrict new books" flag — in Settings →
Metadata Profiles → Library Defaults.

**Hardlinks "don't work".**
Rule 5 — separate mounts. One shared parent mount, then `auto` or `hardlink`
mode.

**Where do I drop files for Bindery to pick up?**
Nowhere (rule 3). Use Manual Import (`/import`) for files it didn't download.

---

More depth: [QUICKSTART.md](QUICKSTART.md) ·
[DEPLOYMENT.md](DEPLOYMENT.md) ·
[Storage & hardlinks](Storage-And-Hardlinks-Wiki.md) ·
[Troubleshooting](Troubleshooting-Wiki.md) ·
[Migrating from Readarr](Migrating-From-Readarr-Wiki.md) ·
[ABS import](ABS-Import-Wiki.md) ·
[Multi-user](multi-user.md)
