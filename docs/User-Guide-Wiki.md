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
- **Import** (`/import`): **In your library** lists the books a library scan
  could not match, for you to adopt in place ([Adopting files already in your
  library](#adopting-files-already-in-your-library)). **From a folder** points
  at a folder of files you already have and matches them to books. Either way a
  file with no catalogue match gets a metadata search, which creates the book
  (and its author, if new) and links the file to it, so an unmatched file is no
  longer a dead end.

Both still end by attaching a file to a catalogue record.

The **Search library** box in the header is the other kind of search: it looks
only at what is already in Bindery. As you type it lists matching authors
(including pen names), books (by title or author) and series, and selecting a
row opens it (a series row opens the Series page with that series expanded,
without scrolling to it). The last row is always *Add "…" to Bindery*, which
opens the **Add to library** dialog with the same text already searched against
your metadata providers, so a miss in the library turns into an add without
retyping. Pressing Enter with nothing highlighted opens that dialog too, even
when the list has library hits; use the arrow keys to pick a hit instead. It
does not query indexers; that stays with the magnifier.

**Where the pages live.** The top bar carries five entries. **Library** holds
Authors, Books and Series; **Activity** holds Wanted, Queue and History (plus
Requests for an admin); **Import**, **Calendar** and **Discover** are pages of
their own. Opening a group lands on its first page and a row of tabs above the
content switches between the rest, so every page keeps the address it always
had. On a phone the menu lists the same pages, indented under their group.

## Five rules that answer most questions

Almost every "why is Bindery doing that?" question comes down to one of these.

### 1. Library Scan is a reconciler, not an importer

**Scan Library** (Settings → General → Library, and automatically every 6
hours) matches files already on disk to books **already in your catalogue**.
It never creates authors or books from files on its own. If you point a fresh
install at a folder of 3,000 epubs and hit Scan, you get a long list of
unmatched books on **Import → In your library** and an empty library, because
nothing populated the catalogue first. That list is where you can adopt them.

The sequence that works is always: **populate the catalogue, then scan**. See
[Bringing in an existing library](#bringing-in-an-existing-library) below.

### 2. Status and Monitored are two different switches

Every book has a **status** — its acquisition lifecycle:

`wanted → imported` (plus `skipped`)

There is no in between status. A grab in flight lives on the Queue page and is
never written onto the book, so a book stays `wanted` until its file is on disk.

Every new book record starts as `wanted`, no matter how it was created. That
is just "no file yet"; it does not by itself make Bindery do anything.

Separately, every book is **monitored** or not. Monitored means "Bindery
actively pursues this." The **Wanted page** — and the automatic search sweep —
only covers books that are *all three of*: status `wanted`, monitored, and not
excluded. A book that is `wanted` but unmonitored shows as "Not monitored" and
is left alone. Flip a single book from the switch next to the status badge on
its own page, or many at once from the Books list (select, then **Monitor** /
**Unmonitor**).

Authors have their own monitored switch, and it outranks the books: **Bindery
never searches for a book by itself while its author is unmonitored**, however
that book's own switch is set. So unmonitoring an author is enough to stop the
sweep grabbing their catalogue, and you do not have to get every book right
first. Searches you start by hand still run, on the book page, from the Wanted
page, from the author page, from series Fill and when you accept a
recommendation, so an unmonitored author is still a library you can fetch from
one book at a time. A Wanted row held back this way says so.

Turning an author's switch off does not rewrite their books' own switches
unless you ask it to. Both the author edit dialog and the Authors page bulk
**Monitor** / **Unmonitor** offer an "apply to existing books" box, unticked by
default; tick it to bring every book of those authors into line in one action.

Two related labels:

- **In Library** = status `imported` = Bindery can see the file on disk.
- **Exclude vs Delete**: a metadata refresh only adds books back for an author
  you monitor and have set to take new items, so for those authors deleting an
  unwanted catalogue book is temporary — the next refresh recreates it.
  **Exclude** is the sticky action for every author: it hides the book, keeps
  it out of searches, and a refresh will not recreate it under a new id either.

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
- Files you acquired outside Bindery are picked up only via **Import → From a
  folder**, or a **Library Scan** and **Import → In your library** for files
  already in the library folder (rule 1 applies).

### 4. Naming templates are for output, not input

The templates in Settings → General → File Naming control how Bindery names
and organises files **it imports itself**. The Library Scan does **not** use
them — it reads existing files with a fixed parser that prefers an
`{Author}/{Book Title}/` folder structure and reads a bare `X - Y` filename as
`Title - Author` (the *opposite* of Readarr's default order). Author folders
settle the order. The Library Scan takes the author from the author folder,
except for an audiobook whose tags name an author: there the tag wins. An
ebook's **title** comes from the file's own name, with the book folder as the
fallback, so a series or box set folder does not retitle the books inside it;
a leading position number is stripped from a folder title, so `01 - The Eye of
the World (1990)` matches as well. An audiobook keeps the folder as the book,
because its files are tracks rather than books. A file
in a folder named after the first part of its name can also be read the other
way round, as author then title, and that reading is kept only if it matches a
book by an author in your library. Bulk folder import tries it for the folder
you point it at, and Manual Import of a single file tries it inside your
library folders, both only when the usual reading matches nothing. The Library
Scan tries it first, in author folders with no book folder below them, so a
Tom Clancy file named author first goes to its own book and not to one with
Tom Clancy in its title. It skips it for an audiobook whose tags name a title,
or an author other than the folder's. Bulk folder import also takes the author
from the folder when the filename has none, or when the filename's author
matches none of the books with that title and the folder name does. Loose files with no author folder still use the
filename alone; if those are misread, move them into author folders, or use
Manual Import, which lets you pick the right book.

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
| **Add to library → an author row** (the **Add Author** button on Authors) | The author **plus their full catalogue** (up to 2,000 titles, after deduplication and the metadata profile's filters), monitored per the monitor mode you pick |
| **Add to library → a book row** (the **Add Book** button on Books or Authors) | One book, and only that book, silently creating its author if needed. Select a search result to review its cover and identifiers before confirming; ISBN lookups show the searched ISBN separately from identifiers reported by the metadata source |
| **Discover → Add to Wanted** | One recommended book |
| **Series → Fill gaps** | The missing books of a linked series, wanted + monitored |
| **Import lists** (Settings → Import / Migrate, Hardcover reading lists) | Every list item, re-synced on the Hardcover list sync interval (Settings → General, 24h by default). Whether the items are also marked wanted is the per-list **Download books from this list** checkbox: on (the default) creates them monitored and queues downloads; off catalogues them unmonitored, so you can browse a Want to Read shelf in Bindery and fetch books one at a time. Authors created by a list never pull their back-catalogue in — only the listed books are added. **Sync now** starts the sync in the background and the row reports its progress, so a large shelf isn't cut short by a request timeout |
| **Library imports** (Calibre, Readarr, ABS, Goodreads CSV, author list) | Your existing catalogue — see the next section |

**Add Author** and **Add Book** open the same dialog. Type an author name, a
title, an ISBN or an ASIN and press Enter; the results list authors first, each
followed by the books the provider attributes to them, with any remaining books
under a *Books* divider. Which button you pressed only changes the placeholder.
What you pick decides what happens next: an author row leads to the monitoring
step (metadata profile, root folder, media type, monitor mode, auto-grab), a
book row to the single-book step (cover, identifiers, format, search on add).
Rows that already match something in your library say **In your library** with
an **Open** link instead of Select, and adding one anyway is refused with a
link to the existing record. An ISBN search, and adding a book row whose
result carries no author id (DNB results, for example), look the ISBN up
across your metadata providers. If the primary provider does not answer during
that lookup, the search or the add is refused with a message to try again once
it responds, rather than offering another provider's record and linking the
book and its author to it for good. A primary that answers without the ISBN
still lets another provider's record through.
Searches run against the metadata providers only
when you press Enter or Search, never as you type; the header library search is
the one that reacts to keystrokes, because it only reads your own catalogue.

Two settings decide whether an author add stays a trickle or becomes a flood:

- **Monitor mode** (per author): *All books* (default), *Future books only*,
  *Latest only*, *None*, or *By series*. With the default, adding a prolific
  author monitors their entire back-catalogue, and every one of those books is
  a search target. If you only want new releases, pick *Future books only*;
  the global default lives in Settings → Metadata Profiles → Library Defaults
  → **Default monitor mode**.
- **Monitor new items** (per author): whether a later metadata refresh may add
  books it discovers at all. *Follow monitor mode* adds them and monitors them
  per the mode; *Don't add them* keeps the refresh to the books you already
  have. Library imports (Calibre, ABS) set *Don't add them* on the authors they
  create, because those catalogues are partial by design.

These two are independent, and monitor mode *None* is not a substitute for the
second one: it means "list the catalogue, monitor none of it", so a refresh
still adds the discovered books, just unmonitored. To stop them arriving at
all, set **Monitor new items** to *Don't add them*. Both settings can be
changed for many authors at once from Authors → select → **Set monitor mode**.

The Books page shows the **whole catalogue** — monitored or not. "Why are
there books here I never asked for?" is rule 2: unmonitored means "won't
grab", not "won't list". Open the author and select the ones you never want there, then
**Exclude** them; the Books page has Monitor, Unmonitor and Delete, but Exclude
lives on the author page.

## From Wanted to your library

**Search.** A scheduled sweep (default every 12 hours; interval in Settings →
General, restart required) searches your indexers for every book on the
Wanted page and auto-grabs the best release. The **Auto-grab** toggle in
Settings → Metadata Profiles → Library Defaults turns grabbing off entirely if you prefer to grab by hand
from the Wanted page. It covers every path that can start a download: the
scheduled sweep, the searches an author add fires, a series fill, adding a
single book, adding from recommendations, a bulk **Search** action, a book
flipping to wanted, and the re-search after a stalled download. A bulk
**Search** refuses while the switch is off and says which setting to change,
keeping your selection; a single book's **Search Indexers** still runs, which
is how you search and grab by hand with grabbing off. Books are
still created and still marked wanted, so the Wanted page is complete when
you come back to it. Searches also fire when an author is added
("Search for books on add") and when a book flips to wanted.

**Two language titles.** A translated book whose title is stored as
"translated / original", such as "El imperio final / The Final Empire", is
searched under the translated part only, because no release is named with
both. This applies when the title is exactly two parts joined by a spaced
slash and the book, or your metadata profile, names a language other than
English. An English bundle such as "Second Nature / One Summer" is searched
whole.

**Daily query limits.** A sweep searches every wanted book against every
enabled indexer back to back, so on a large library it can be thousands of
requests in one burst. Private trackers with a daily API allowance do not
appreciate that, and an allowance spent by Bindery is an allowance your other
services cannot use. Set a **Daily query limit** on the indexer in Settings →
Indexers and Bindery stops searching it once it has been sent that many requests
in the last 24 hours, then picks up again as the oldest ones fall out of the
window. Requests are counted in hourly blocks, so capacity returns an hour at a
time rather than all at once, and a block is released once it is a full hour
past the window: 1000 requests spent at 09:30 today free up at 10:00 tomorrow,
not 09:30. Erring that way means the limit binds slightly early rather than
slightly late, which is the safe direction when the allowance is not Bindery's
to overspend. Leave it blank for no limit, which is what every indexer starts
with. The number counts requests rather than books, because one book costs
between one and eight depending on how far the search has to fall back from a
structured query to a plain text one. The Indexers tab shows how much of the
limit is spent, a capped indexer is listed as skipped with the reason in the
search details panel, and the **Test** button is exempt so it still works when
you are trying to diagnose a quiet indexer. Raising the search interval is not
an alternative: it changes how often the burst happens, not how big one burst
is.

**Rate limits.** When an indexer refuses a search because a request limit
was reached, whether as a Newznab "request limit reached" error or as an HTTP
429 from the host in front of it (Cloudflare's "error code: 1015"), Bindery
stops searching that indexer for the time the indexer asked for, or for an hour
when it gave no time. An indexer that limits again after that is left alone
for longer each time: three hours, then six, twelve and a day, and every search
it answers brings it back down a step. The Indexers tab shows a held indexer
with the time searches resume; editing the indexer clears the hold, and so does
a restart. A daily query limit stops the burst before the indexer has to refuse
it; the hold is what happens when it refuses anyway.

**Decision.** Each release is checked against your quality profile (allowed
formats), delay profile, blocklist, size limits, and language filter. A
quality profile is an allow list only; the order you put the formats in is
not read. When more than one allowed format is found, Bindery prefers its
built in ranking, best first: azw3, epub, mobi and azw, pdf, rtf, txt for
ebooks, and flac, m4b, m4a, mp3 for audiobooks.
On indexers marked *freeleech only*, non-freeleech releases are not discarded
— they are parked as **pending** for manual approval.

**Multi-book packs are not auto-grabbed.** A download is linked to exactly
one book, and the importer works out one destination folder from it, so there
is no correct way to import a release that is several books: everything in it
would land in one book's folder. Automatic selection therefore skips releases
that name themselves as a pack — an explicit range like `Books 1-4`, a box
set, an omnibus, a "complete series". They still appear in interactive search
so you can see them, and if you grab one by hand the import is blocked with an
explanation rather than run. To take a pack, point **Import → From a folder** at the
finished download and place each book's files against the right book record.

A release is only judged a pack on wording that single books do not use about
themselves. `Part 1-2` is left alone, because that is how one long audiobook
is split, and so is `Trilogy`, because real single books are subtitled "Book
III of the X Trilogy". If the book you are tracking is itself a bundle (a box
set as one record), matching packs are allowed for it.

**Grab.** Torznab (torrent) indexers route to your torrent client, Newznab
(usenet) to your NZB client — which is why the client's protocol must match
the indexer, and why the category (default `books`) must already exist in the
client. Bindery fetches the .torrent/NZB itself and hands it over.

**Import.** When the client reports the job complete, Bindery matches it to
the book, places the file per your import mode and naming template, and marks
the book **In Library**. Ebooks land under the author's root folder (falling
back to the default root folder, then `BINDERY_LIBRARY_DIR`); audiobooks have
their own destination chain (`BINDERY_AUDIOBOOK_DIR`, per-author override).
Every author added through the UI gets a root folder written on the author
itself, seeded from the default you set in Settings, so changing that default
later moves only authors you have not created yet. In 1.32.1 and earlier the
dialog seeded from the first root folder in the list instead of from the
setting, which made the setting unreachable for authors added that way (#2166);
correct one from the author's own edit dialog.
After import, Bindery fans out to whatever integrations you enabled: Calibre,
a CWA ingest folder, Grimmory's BookDrop, an Audiobookshelf library scan,
webhooks. The three ways of reaching Calibre or CWA are easy to mix up; the
[Calibre integration guide](Calibre-Integration-Wiki.md) tells them apart.

If the library app downstream reads sidecar metadata, turn on **Write a
metadata.opf sidecar** in Settings → General (off by default). Bindery then
writes a Calibre style `metadata.opf` next to each imported ebook and
audiobook with its own title, author, series and identifiers, and refreshes it
on Reorganize. The book file itself is never modified.

**Queue and History.** The Queue page shows live downloads and, importantly,
the recovery actions: **Retry import** (after fixing a path remap), **Match to
book** (attach a failed import to the right book and import it from disk), and
per-row error detail. History records every grab/import/failure and can
blocklist a bad release in one click. Blocked releases are listed under
Settings → Blocklist, where you can remove one to let it be grabbed again.

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
| A Readarr install | Settings → Import / Migrate → upload `readarr.db` ([guide](Migrating-From-Readarr-Wiki.md)) |
| An Audiobookshelf server | Settings → Audiobookshelf → configure + **Import** ([guide](ABS-Import-Wiki.md)) |
| A Goodreads account | Settings → Import / Migrate → **Goodreads CSV** (export, filter by shelf, preview, commit) |
| Just a list of authors | Settings → Import / Migrate → **Upload CSV**, one author name per line |
| Only folders of files | Scan the library, then adopt on **Import → In your library**; or use **Import → From a folder** for files outside the library |

Then run **Settings → General → Library → Scan Library** to attach your files
to the records. Things worth knowing before you judge the results:

- Library imports create **records only** — no covers, no descriptions, no
  files. Run **Refresh metadata** on authors to fill in covers and details,
  and the scan to attach files. "Fresh import looks empty" is expected, not
  broken. The Calibre library import is the exception on the files half: it
  reads the path of every format from `metadata.db` and tracks it directly, so
  those books arrive with their files already attached and do not need a scan
  to find them (#1635). In 1.32.1 and earlier it recorded nothing, which left
  a Calibre-managed book looking imported while Bindery tracked no file for it.
- The Calibre library import is also the exception on covers. Each book's
  `cover.jpg` from the library folder is copied into Bindery's data directory
  (`covers/` under `BINDERY_DATA_DIR`) and shown for the book and every one of
  its editions, so a Calibre library has covers straight after import, with
  or without a metadata provider match. A cover a provider has already supplied
  is kept; the Calibre cover only fills the gap, and **Refresh metadata** can
  still replace it. Earlier versions recorded the library path instead, which
  the browser could not load, so Calibre-imported books had no cover at all
  (#2564). On the first start after upgrading, Bindery copies those covers in
  and repairs the existing rows in the background; the library must be
  mounted at the same path for that pass, and any it cannot read are picked
  up by the next start or the next library import.
- The Calibre library import reads `metadata.db` read-only and honours
  Calibre's write-ahead log, so an author you merged or a book you deleted in
  Calibre, Calibre-Web-Automated or `calibredb` is gone from the next import
  even before Calibre has checkpointed it back into `metadata.db` (#2631).
  There are two exceptions. A library directory mounted read-only into the
  container with no `metadata.db-shm` file beside the database (Calibre not
  running), and a library on a network filesystem such as NFS or SMB, where
  SQLite cannot share the write-ahead-log index. In both cases Bindery
  falls back to reading the last checkpoint and logs a warning saying
  Calibre edits will not show until Calibre checkpoints. For the first,
  mount the library writable or keep Calibre running; for a network mount
  there is no workaround on the Bindery side, and Calibre itself does not
  recommend keeping a library on one.
- The scan only matches files whose **author already exists** in Bindery, by
  normalised name — `B. Sanderson/` on disk won't match a "Brandon Sanderson"
  author row.
- A Calibre book credited to several people is filed under the **first**
  author only. Bindery books carry a single author, so the other credits are
  not recorded. Those co-authors still get their own author page as soon as a
  book credits them first. Older versions filed them as aliases of the primary
  author instead, which silently swallowed their whole catalogue: see
  [Troubleshooting](Troubleshooting-Wiki.md#books-are-filed-under-the-wrong-author-after-a-calibre-import).
- ABS imports that "lose" titles usually didn't: ambiguous matches are parked
  in the **review queue** (Settings → Audiobookshelf) for you to resolve, and
  the import summary counts them.
- Books the scan could not match wait on **Import → In your library**, one row
  per book with a sentence saying why and what to do: add the author, confirm
  a suggested book, or choose one. See [Adopting files already in your
  library](#adopting-files-already-in-your-library).
- **Fix match moves and renames the file.** When a book page shows the wrong
  file, the **Fix match** button reassigns it to the book you pick. That runs
  the full import, so the file is moved into the target book's folder and
  renamed from your naming template, replacing your own layout for that file.
  The modal warns you and shows the exact destination path before you confirm,
  and nothing happens until you do; the move itself then runs in the background
  and Bindery cannot undo it for you. Reassigning the metadata link *without*
  relocating the file is not available yet (#2055).
- A folder holding both an ebook and an audiobook for the same book attaches
  both in a single scan — one file per format, so a second scan is not needed.
- A PDF, TXT, RTF, CBZ or CBR sitting in a folder that also holds audio is treated as
  an **audiobook supplement** (the companion PDF Audible-style releases ship)
  and is not attached as the book's ebook. The same file in a folder with no
  audio in it is treated as an ebook as usual.
- When one of those same file types competes with a **real ebook** (EPUB, MOBI,
  AZW3, FB2, DJVU) for the same book, the real ebook always wins and the other
  file is counted as already tracked rather than listed as unmatched — a
  `Book (notes).txt` beside `Book.epub` can no longer take the book's ebook slot
  by sorting first in the folder. Nothing is excluded outright, so a library of
  TXT, RTF or PDF files still attaches normally: those files are only passed
  over when a better file for the same book exists.
- The same two rules apply when **adding a book** and Bindery checks whether you
  already own it: a cue sheet or notes file next to an audiobook is never taken
  as evidence you own the book, and a real ebook wins over a supplement-class
  file when both match (#2240).

## Adopting files already in your library

A library scan attaches every file it can match with confidence and leaves
the rest for you. Those books wait on **Import → In your library**, the page
`/import` opens on. Each row is one **book**, not one file: a 193 track
audiobook folder is one row, a folder whose audio subfolders are all discs
(`CD1`, `Disc 2`, `Disk 3`) is one row named after it, and `Dune.epub` beside
`Dune.mobi` is one row. An `Artwork` folder without audio, or a hidden or
system folder such as `@eaDir`, does not stop a disc set from grouping.
Folders named `1`, `2`, `Book 1` or `Part 1` stay separate rows; when they are
really one book, adopt each of them into it.

**Adopting registers the files where they are.** Nothing is moved, renamed or
queued, and no indexer search starts. It is the scan's own match with you
supplying the answer, so **Undo** can take it back exactly.

How to work through the list:

- **Strong match** means the title is very close and the author is the same.
  **Confirm** adopts it in one click.
- **Possible match** means the title is only similar, or the author differs.
  Click the suggested title to check it in the editor, where it is already
  selected, and adopt it from there.
- **Choose book** opens the row in place: the suggestions with their scores,
  a search of your library (prefilled from the file), and a collapsed
  **Search metadata**. Metadata providers are only asked when you press Search
  there, so opening rows never spends provider quota. **Add and adopt** adds
  the book from metadata and adopts the files in one step.
- **Books whose author is not in your library** and that share a folder are
  one row: that is one decision. **Add author**, then **Scan now**, and the
  scan attaches what it can by itself. Open the row to see its books, which
  you can still choose or ignore one by one. Its **More** menu has **Ignore
  folder** to set all of them aside.
- **More** on any row holds the rest: Choose book, Show files, Ignore.
  **Ignore** hides a row that is not a book you want tracked. Later scans keep
  it hidden. The **Ignored** list brings any of them back.
- **The folder list** on the left filters the table to one author folder. An
  amber dot marks a folder whose author is not in your library.
- Each row says in one line what the scan found. Hovering it shows the full
  explanation and the scanner's reason code, for bug reports; the editor
  shows the explanation too.
- Keyboard: arrow keys move between rows, **Enter** opens one, **Esc** closes
  it, **i** ignores, **u** undoes, **/** jumps to the search.

What adopting does to your library:

- Choosing a book **already in your library** attaches the files to it and
  changes nothing else: its owner and its monitored flag stay as they were.
- A book added from metadata is added **unmonitored**, in the format you
  adopted (ebook or audiobook), so Bindery never goes looking for the other
  format behind your back. It and a new author are owned by the admin who
  adopted them. The author's other books are not added.
- **Undo** removes exactly the file entries the adoption made, and only while
  each still belongs to the book it was adopted into; a file that has since
  moved to another book stays with that book. A book or author the adoption
  created is removed too, unless something else now depends on it (another
  file, another book by that author, excluded or not, another adopted row),
  or the book has been used since: monitored, edited, linked to a series, or
  searched for or downloaded. Then the book stays and Undo says so.
- If Bindery stops in the middle of an adoption, the next start reverses what
  that adoption had done and the book is back in **Needs a decision**.
- Only an admin can see or act on this list, because it shows server paths.

Things worth knowing:

- Only regular files inside your library folders are listed. A symlink is not
  adopted, including one inside the library that points elsewhere.
- A scan that finds no files at all (an unmounted volume, say) changes
  nothing on this list, so your ignores and adoptions survive it.
- An adopted row stays, with Undo, for as long as its book exists. An ignored
  row is forgotten 30 days after a scan last saw its files, counted only by
  scans that found files in that row's library folder, or once that folder is
  no longer one of your library folders at all.
- One scan lists up to 20,000 books from up to 50,000 unmatched files. A
  larger library says so; adopt or ignore some and scan again.
- **From a folder** (`/import?view=folder`) is the other way in: point it at a
  folder anywhere Bindery can read, such as your downloads, and it imports
  what it matches into the library, moving or copying the files.

## Metadata: where book data comes from

- **OpenLibrary** is the default primary provider — it decides what an
  author's catalogue looks like. It is community data: expect occasional
  duplicates, language mix-ups, and box-set entries. The "primary" selector
  (Settings → Metadata Profiles → Library Defaults) offers OpenLibrary,
  **DNB** (German National Library), and **Hardcover**.
- **Hardcover** is an enricher by default — it improves search results,
  ratings, and series data, and powers import lists and the Discover wishlist
  row. **Without an API token (Settings → API Keys) Hardcover is silently
  skipped everywhere.** The free token is the single highest-value config for
  metadata quality.

  It can also be promoted to *primary*, which is worth doing if OpenLibrary
  refreshes bury your wanted list in translations and box sets. Hardcover's
  catalogue is editorially curated: refreshing a prolific author typically
  yields only the English-language works, without the translated editions,
  omnibus bundles, alternate-title duplicates, and non-book merchandise rows
  OpenLibrary returns as separate works. The trade-off is a thinner long tail
  — obscure, self-published, and very old titles are more likely to be missing
  — and the API token becomes load-bearing rather than optional, so the
  selector stays disabled until you save one.
- **Google Books** (free API key) and **Audnexus/Audible** (audiobook
  narrator, duration, by ASIN) enrich further.

For a Hardcover audiobook, the chosen audio edition can fill a missing book
duration before indexer search. Audnex may update that duration when an ASIN is
available. Explicit audio formats take priority over an unknown format with a
runtime; runtime breaks ties between equally ranked editions. A runtime alone
does not turn a known print format into an audiobook. Edition hydration respects
a manually locked language, including a language deliberately cleared to empty.

Which of those a given book actually came from is on the book page, under
**Metadata source**. It names the provider, shows the identifier the book is
bound to with a copy button, and lists any other provider ids the same book is
known by. That is the thing to check before deciding a book needs re-binding,
and the id is what to quote in a bug report. Hover or activate **Links** while
confirming a book in the Add to library dialog or in the book header to open
trustworthy upstream pages for OpenLibrary, Google Books, Hardcover, and DNB
records.
Calibre and Audiobookshelf ids remain visible only under **Metadata source**
because they do not map to stable public pages.

When metadata is wrong, you have three levels of fix:

1. **Edit metadata** on the book — edited fields are **locked** so refreshes
   never overwrite them ([guide](Metadata-Editing-Wiki.md)).
2. **Re-bind** the book, or **relink** the author ("Find better match"), to a
   different provider record when the match itself is wrong.
3. A **metadata profile** (languages, minimum page count, skip part books)
   filters what a catalogue sync lets in.

Box sets need no setting. A work whose title plainly names a bundle ("... Box
Set", "3 Books Set", "Carton of 10 Signed Copies") is dropped from every
author catalogue as it is fetched, on any provider. **Skip part books** in the
metadata profile adds the shapes that are a judgement call, and is off by
default because each of them has real single books it would wrongly catch: a
title ending in "Omnibus", slash-separated titles like "Title A / Title B",
and "Books 1-3". Neither filter touches "Trilogy". A bundle already in your
library is left alone; it just stops being offered back by the catalogue.

A **metadata refresh** re-syncs an author's metadata from the provider:
covers, descriptions, ratings, genres and series links on the books you
already have. It only *adds* newly-discovered works for an author you monitor
and have set to take new items — an unmonitored author, or one set to *Don't
add them*, is refreshed in place and never grows. (Exception: an author with
no books at all is populated, which is how bulk **Refresh metadata** repairs
an import that landed an author but no catalogue.) When a refresh declines to
add works, the author page says how many and why.

**Refresh metadata** on a single author asks the metadata providers for
current data: the bio, the photo, the book list and, when the default media
type is audiobook or both, the Audible catalogue. The page waits for the
refresh to finish and then shows the result, so a bio, photo or new book added
upstream shows up on the first click. The page waits up to a minute; a refresh
that takes longer carries on in the background, and reloading the page later
shows it. Clicking Refresh again while a refresh for that author is still
running waits for that one instead of starting another. If the refresh
already running was a scheduled, bulk or Refresh all one, which read the
cached copy, the page waits for it to finish and then runs its own refresh.

When a provider fails outright, Bindery keeps the copy it fetched in the last
24 hours rather than replacing it. How well a refresh can spot a catalogue
that came back short depends on the provider:

- **OpenLibrary** reports when a request failed along the way. Bindery then
  keeps the earlier copy and adds any new books the partial answer did
  include.
- **Other providers** (DNB, for example) cannot say their answer is short. An
  empty book list is treated as a failure while Bindery holds an earlier
  copy, so it never wipes the catalogue, but a list that is merely shorter
  than before is taken as the current catalogue.
- If the **Hardcover** supplement fails, the refresh uses the earlier copy of
  the catalogue as it is, because Hardcover is what identifies the box sets
  and omnibus editions to leave out.

**Refresh all metadata**, the bulk Refresh action and the scheduled refresh
reuse what Bindery fetched in the last 24 hours instead, which keeps a whole
library refresh from hammering the providers; a change upstream reaches them
within a day.

### New releases arrive on their own

Following an author means their next book shows up without a click. Bindery
can check each monitored author's catalogue on a schedule and add the books it
does not have yet. It **ships off**: nothing is checked until you pick an
interval, and **Weekly** is the one to pick if you are not sure. This is the
same sync as **Refresh metadata**, so the same rules apply: the author must be monitored and set to
take new items, the metadata profile's language and junk filters still run,
and each new book is monitored or not according to the author's monitor mode.

- **Turning it on and how often:** Settings → General → **New release
  discovery**. It starts on Off; choose Daily, Weekly or Monthly to turn it
  on, and Off again to stop it. The change applies within the hour, no
  restart.
- **How it spreads out:** every hour Bindery checks a small share of your
  authors (at most 25), so a week's worth of checks is spread over the week
  instead of arriving in one burst. Authors never checked go first. One
  author gets at most 10 minutes; one that takes longer is counted as checked
  and waits for its next turn.
- **Your refreshes come first:** while **Refresh all** or a bulk refresh of
  selected authors is running, discovery stops until the next hour. A
  **Refresh metadata** click on an author discovery is checking right then
  says so; try again a minute later. A bulk refresh that reaches that author
  waits while the check writes its new books, so nothing is added or
  announced twice. That wait lasts at most as long as one author's check, 10
  minutes at worst. Adding a single book never waits for it.
- **Changes since the hour started:** an author you unmonitor, delete or set
  to *Don't add them* while a pass is running is skipped.
- **Grabbing:** discovery only adds books. A new monitored book is picked up
  by the next wanted search, and only when **auto grab** is on.
- **Opting an author out:** set their **Monitor new items** to *Don't add
  them*. Unmonitored authors and Calibre library authors are not checked
  either.
- **When a provider struggles:** when OpenLibrary or Hardcover refuses with a
  rate limit, the pass stops and the remaining authors wait for the next
  hour. When three authors in a row fail because the provider is down (server
  errors, network failures, timeouts), the pass stops too, and those three
  are tried again in about six hours rather than a week later. An error about
  one author, such as an author the provider no longer knows, counts that
  author as checked, so broken authors cannot hold up everyone else.
- **Covers:** discovery looks up covers only for the books it adds. A book you
  already have that has no cover gets one from **Refresh metadata**, not from
  discovery, which saves a provider call for every such book.
- **Getting told:** a webhook with **New book** turned on receives one
  `bookAnnounced` message per author run that added books to an author you
  already had, listing up to ten titles. It is **off for every webhook until
  you turn it on**, existing ones included. The first fill of a newly added
  author, refilling an author whose books you had all deleted, and adding a
  single book never send it.

**A risk worth knowing.** OpenLibrary can be edited by anyone. A false "new
book" added to an author you follow becomes a Wanted, monitored book, and with
auto grab on the next wanted search will try to download it. This could
already happen when you clicked Refresh; discovery makes it happen without
you. The profile filters, the small hourly batch and the `bookAnnounced`
message are the mitigations, and *Don't add them* on an author, or leaving
discovery Off, removes it entirely.

Discovery follows authors only. Watching a **series** for its next entry is
planned separately
([#2523](https://github.com/vavallee/bindery/issues/2523)). The **Add to
shortlist** toggle on a series marks it so you can find it again; it does not
make Bindery check the series.

Changing a provider or tightening a metadata profile does not silently delete
old catalogue rows during refresh. To apply the new catalogue rules to an
author's existing rows, open the author, choose **More → Reconcile catalogue…**,
and review the preview. Every candidate starts selected; clear any row you do
not want to remove before applying. Apply removes only selected metadata-only
rows whose status is still Wanted. Imported books, excluded books, books in
another active status, and any row with `filePath`, `ebookFilePath`,
`audiobookFilePath`, or a tracked `book_files` entry are protected. No files
are deleted. Apply fetches the provider again and rechecks the database
safeguards, so a row that gained a file or changed status after preview is
skipped. If the provider returns a partial catalogue, missing works are kept
rather than guessed stale. OpenLibrary's `searchAuthorWorks` lookup currently
requests at most 200 works (`limit=200`), so authors with more than 200 works
remain marked partial: the warning may stay visible, and reconciliation will
not remove their `not_in_current_catalogue` rows.

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
and Unmonitor or Exclude. Before adding more, pick a different mode on the Add
Author dialog (it shows how many books will arrive and what will be searched
for), or change the default in Settings → Metadata Profiles → Library
Defaults. *None* lists the catalogue and searches for nothing. *Future books
only* searches only for unreleased titles, and with scheduled discovery turned
on new releases join the list on their own
([New releases arrive on their own](#new-releases-arrive-on-their-own)).

**Scan Library sees my files but imports nothing.**
Rule 1: the catalogue is empty or the authors don't exist yet. Populate
first ([Bringing in an existing library](#bringing-in-an-existing-library)),
then scan, or adopt the books from **Import → In your library**
([Adopting files](#adopting-files-already-in-your-library)).

**I moved my files and a book still shows the old path.**
Fixed (#2186). A book now shows whichever of its tracked files still exists,
and a **Scan Library** run repairs books that were already stuck on a dead
path. The old entry stays listed under the book's **Files**; **Forget this
file** clears it without touching the disk. Use **Rename files** rather than
moving things by hand and it never happens.
([troubleshooting](Troubleshooting-Wiki.md))

**I added one book and got the author's whole back catalogue.**
Fixed (#1816). Adding one book adds one book; a refresh only adds
newly-discovered works for an author you monitor and have set to take new
items (#1815). Rows an older version pulled in are ordinary books — bulk-select
them on the author page and Delete or Exclude.

**I deleted books I don't want and they came back.**
Delete is undone by the next metadata refresh, for an author still set to take
new items. It is not undone for an author you unmonitored or set to *Don't add
them* — including one whose books you deleted all of. Use **Exclude** if you
want the book gone regardless of how the author is monitored later.

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
from inside the container. Settings → Download Clients → **Diagnose** walks the
connection, the category, the save path, the path remap and the hardlink check
and names the first thing to fix. Copy report leaves out the host, port and
username, so it is safe to paste into an issue.

**Bindery is behind my VPN and metadata broke.**
OpenLibrary blocks many VPN/datacenter IPs. Keep the torrent client behind the VPN.
Bindery needs to reach whatever you configured as an indexer, and it fetches
each .torrent or NZB itself before handing it to the client, so if your indexers
are direct Newznab or Torznab endpoints rather than Prowlarr, Bindery reaches
them too. Gluetun users: allow LAN with
`FIREWALL_OUTBOUND_SUBNETS`, or ABS/Calibre connections will time out.

**The book has my ebook but still shows as not done.**
Its media type is *Both*, so it also wants the audiobook (each format has its
own lifecycle). Set the book to *Ebook* on the Books page, or set the default
media type — and optionally the "restrict new books" flag — in Settings →
Metadata Profiles → Library Defaults.

How it got to *Both* without you asking is worth knowing if you are on an older
release. An author refresh merges two catalogue records for one work into a
single dual-format row, so that one book does not become two, and up to 1.32.1
it did that even to books already on disk. One reporter had a routine refresh
flip 29 owned books in a single run. It no longer touches the format of a book
that already has its file (#2096); a book nobody has yet still merges, which is
what the merge is for. Books already widened keep their media type, so set those
back by hand and the status follows.

**Hardlinks "don't work".**
Rule 5 — separate mounts. One shared parent mount, then `auto` or `hardlink`
mode.

**Where do I drop files for Bindery to pick up?**
Nowhere (rule 3). Use **Import** (`/import`) for files it didn't download:
**From a folder** for files elsewhere, **In your library** for files a library
scan found but could not match.

---

More depth: [QUICKSTART.md](QUICKSTART.md) ·
[DEPLOYMENT.md](DEPLOYMENT.md) ·
[Storage & hardlinks](Storage-And-Hardlinks-Wiki.md) ·
[Troubleshooting](Troubleshooting-Wiki.md) ·
[Migrating from Readarr](Migrating-From-Readarr-Wiki.md) ·
[ABS import](ABS-Import-Wiki.md) ·
[Multi-user](multi-user.md)
