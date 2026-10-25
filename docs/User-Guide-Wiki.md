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
  at a folder of files you already have and matches them to books; files already
  tracked in the library are skipped by default, and toggling **Show already
  imported** surfaces them again, e.g. to spot a corrupted file or relink one.
  Either way a file with no catalogue match gets a metadata search, which
  creates the book (and its author, if new) and links the file to it, so an
  unmatched file is no longer a dead end.

Both still end by attaching a file to a catalogue record.

The **Search library** box in the header is the other kind of search: it looks
only at what is already in Bindery. As you type it lists matching authors
(including pen names), books (by title or author) and series, and selecting a
row opens it (a series row opens the Series page with that series expanded and
scrolled into view; so does a series heading on an author page that is grouped
by series). The last row is always *Add "…" to Bindery*, which
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

The scan leaves out folders that are not library content: known junk and sync
folders (`.torrents`, `.Trash-*`, `.Trashes`, `.incomplete`,
`.bindery-stage-*`, `.AppleDouble`, `.AppleDB`, `.Spotlight-V100`,
`.fseventsd`, `.TemporaryItems`, `.DocumentRevisions-V100`, `.thumbnails`,
`.stfolder`, `.stversions`, `.syncthing.*`, `.sync`, `.git`, `.svn`,
`.recycle`, `.snapshot`, and the NAS and file system folders `@eaDir`,
`#recycle`, `.@__thumb`, `.zfs`, `.snapshots` and `lost+found`, in any
letter case), any folder holding a
`.binderyignore` file (an empty file is enough; everything under that folder
is skipped), and the download folders (`BINDERY_DOWNLOAD_DIR`,
`BINDERY_AUDIOBOOK_DOWNLOAD_DIR`) when they sit inside a library folder. Other
folders that start with a dot are scanned, because a book folder can start
with one (`...And Then There Were None`, `.hack Sign`); to leave one out, put
a `.binderyignore` file in it. The same goes for the check that skips
searching for books you already have, and for importing a folder from
**Import**. Entries an earlier scan listed from those folders disappear on the
next scan.

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

- **In Library** = Bindery can see at least one file for the book on disk. That is
  status `imported`, and also a `wanted` book of format *Both* that already has
  one of its two formats: it stays on the Wanted page while the other format is
  missing, and it is listed under In Library as well (#3132). The Books page,
  an author page's In library count and the have / total number on the Authors
  list all use this one rule. With a format filter set, the book has to own
  that format, so Audiobook plus In Library does not list a book that only has
  an ebook.
- **Exclude vs Delete**: a metadata refresh only adds books back for an author
  you monitor and have set to take new items, so for those authors deleting an
  unwanted catalogue book is temporary — the next refresh recreates it.
  **Exclude** is the sticky action for every author: it hides the book, keeps
  it out of searches, and a refresh will not recreate it under a new id either.
  To bring excluded books back, set the author page's status filter to
  **Excluded**, tick the books and choose **Un-exclude**; a single book has the
  same button on its own page.

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
title, an ISBN or an ASIN and press Enter. The **All / Books / Authors** control
under the search box picks what the results show: *Books* is a flat list of
books, best title match first; *Authors* is author rows only; *All* lists
authors first, each followed by the books the provider attributes to them, with
any remaining books under a *Books* divider. **Add Book** opens on *Books*,
**Add Author** on *Authors*, and the header search on *All*. Every search looks
up both kinds, so switching views is instant, and each view shows how many
results it holds. Which button you pressed otherwise only changes the
placeholder. What you pick decides what happens next: an author row leads to the monitoring
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
single book, adding from recommendations, a bulk **Search** action, a book's own
**Automatic search**, a book flipping to wanted, and the re-search after a
stalled download. A bulk **Search** refuses while the switch is off and says
which setting to change, keeping your selection; so does a book's
**Automatic search**. A single book's **Search** still runs, which
is how you search and grab by hand with grabbing off. Books are
still created and still marked wanted, so the Wanted page is complete when
you come back to it. Searches also fire when an author is added
("Search for books on add") and when a book flips to wanted.

**Two search buttons on a book page.** They do different things, and for a
long time only the first one existed (#2668). **Search ebook indexers** (the
wording follows the book's media type) is interactive: it queries every
indexer, shows you the releases with their scores, and grabs nothing until you
press Grab. **Automatic search** is the sweep's own behaviour for this one
book: Bindery picks the best release itself, sends it to your download client,
and tells you to watch the Queue and History. Use the first when you want a
particular release or a particular format, the second when you just want the
book. Automatic search appears only while the book still needs a format it is
monitored for, because that is the point at which the automatic path has
something to look for; once every monitored format is on disk it would do
nothing, and the interactive button is the one that can still get you a
different copy.

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
quality profile is two ordered lists, ebook formats and audiobook formats;
only ticked formats may be grabbed, and when more than one ticked format is
found the one nearest the top of its list wins, on the scheduled sweep and on
the book page alike.
On indexers marked *freeleech only*, non-freeleech releases are not discarded
— they are parked as **pending** for manual approval.

**Quality profiles.** Settings → Quality Profiles. Each profile holds an
ebook list and an audiobook list, numbered from the top, and top is best.
Tick a format to allow it and untick it to refuse it; an unticked format is
never grabbed and never counts in the ranking, wherever it sits. Move a format up or down to change
which one Bindery prefers. A release that carries several formats, such as
"azw3 epub", counts as the best ticked one it carries. When a book ends up with
several ebook files, the one served and offered over OPDS is chosen in a fixed
order, EPUB, KEPUB, AZW3, MOBI, PDF and then the rest, and every file is kept. A list with nothing in
it means the profile has no opinion on that kind: any format of that kind is
accepted and ranked by the built in order, which is azw3, epub, mobi and azw,
pdf, rtf, txt for ebooks, and flac, m4b, m4a, mp3 for audiobooks. That same
built in order ranks every search for an author with no quality profile at
all. A list with entries but nothing ticked means nothing of that kind is
grabbed for the author. Profiles made
before this rule existed were reversed once on upgrade, so a profile you
never reordered now reads best first and prefers what it always did.

**Multi-book packs are not auto-grabbed.** A download is linked to exactly
one book, and the importer works out one destination folder from it, so there
is no correct way to import a release that is several books: everything in it
would land in one book's folder. Automatic selection therefore skips releases
that name themselves as a pack — an explicit range like `Books 1-4`, a box
set, an omnibus, a "complete series". They still appear in interactive search
so you can see them, and if you grab one by hand the import is blocked with an
explanation rather than run. To take a pack, point **Import → From a folder** at the
finished download and place each book's files against the right book record.
A release that is not named as a pack can still hold several books. The name
check cannot see that, but the content check on the finished download can (see
below).

A release is only judged a pack on wording that single books do not use about
themselves. `Part 1-2` is left alone, because that is how one long audiobook
is split, and so is `Trilogy`, because real single books are subtitled "Book
III of the X Trilogy". A bare `series` or `collection` is left alone too, but
the shapes only a pack uses are caught: `Foundation series by Isaac Asimov`
when only pack or format words (EPUB, `7 Books`, `Full Series`, `eBook Pack`,
`Kindle Edition`, `Updated 2021`) or a list of two or more commas or three
titles joined by `and` follow the author, a range after the series word such
as `Discworld Series 1-41`, `Series (1-7)` or `Series - Isaac Asimov 1-7`, a bracketed count of two or more such as
`(11 Books)`, `Books Collection` (plural only), `Series Pack` and `Saga eBooks`.
A single title after the author names one book and is not a pack: `Discworld
Series by Terry Pratchett - Guards! Guards!`, `Series by C.S. Lewis, The
Magician's Nephew`, `Series by Pierce Brown, Book 1`, `[1 Book]`, a spaced
`Series 1 - 2`. If the book you are tracking is
itself a bundle (a box set as one record), matching packs are allowed for it.

A pack whose name gives no sign of it can still be grabbed. When a download
holds several ebook files of one format, the one whose file name or EPUB title
is closest to the book is imported, and so is the one an external drop folder
receives, in both its flat and templated layouts. A file is only moved ahead of
the one that would have gone first when its name or title clearly matches the
book, for example `Wheel of Time 01 - The Eye of the World` for the book `The
Eye of the World`; names that say nothing keep the order they have on disk. An ebook file under 4 KiB in a download, such as the notes or link
`.txt` a release ships beside the book, is not imported as a second format. A
download that holds nothing else is blocked, whether the import is managed or
goes to an external drop folder, and Manual Import places a file that small if
it really is the book, in either mode. A manual import of such a file for a
book set to both formats is still held for its sibling and released with it. Audiobook tracks are never judged by size.

**Grab.** Torznab (torrent) indexers route to your torrent client, Newznab
(usenet) to your NZB client — which is why the client's protocol must match
the indexer, and why the category (default `books`) must already exist in the
client. Bindery fetches the .torrent/NZB itself and hands it over.

**Pinning an indexer to a download client.** Settings → Indexers has a
**Download client** dropdown on every indexer. Leave it on *Default* and
nothing changes: Bindery picks the client by protocol, priority and media type
as before. Pick a client and every grab from that indexer goes to that client,
whether it came from the scheduled search, the search page, the Pending tab, or
Retry download on the Queue. This is how you keep a private tracker's torrents
on a separate qBittorrent (its own VPN, port or seeding policy) from your public
one. The list shows only enabled clients of the indexer's protocol, and saving a
pin to a client of the other protocol is refused.

* **A pin wins over everything else.** It overrides the client's priority and
  its media type or category routing: an audiobook from a pinned indexer goes to
  the pinned client even if another client has an audiobook category.
* **There is no fallback.** If the pinned client is disabled, missing or of the
  wrong protocol, or does not answer, the grab fails with a message naming the
  indexer (and the client too, if you are an admin), and nothing is sent to any
  other client. A scheduled search skips that indexer's releases, still grabs
  from your other indexers, and records the failure in History once a day per
  book and indexer. The indexer's row in Settings shows an amber warning while
  its pinned client is disabled, missing or the wrong protocol. Enable the
  client or pick another one and the next grab goes through. A grab with no
  indexer id (an API caller) is tied to its indexer by the release URL's host;
  see the troubleshooting page for the limits.
* **A pinned client cannot be deleted.** Deleting a download client that an
  indexer is pinned to is refused (HTTP 409) with the indexer names. Choose
  another client for those indexers, or set them back to *Default*, first.
  Disabling the client is allowed.
* **A Prowlarr sync keeps the pin.** It is local configuration like priority and
  the seed limits.

**Seeding limits.** A torrent indexer can carry its own seeding rules in
Settings → Indexers, so a private tracker's minimum is met without changing the
client's global rules for everything else. **Seed ratio** stops seeding at that
upload ratio (or never, with *Unlimited*). **Seed time** stops seeding after
that many minutes in total, and **Inactive seed time** after that many minutes
without uploading. Leave a field blank to keep the client's own rule. They are
applied to each torrent as it is grabbed, so changing them affects later grabs,
not torrents already in the client. Indexers synced from Prowlarr pick up
Prowlarr's per indexer seed ratio and seed time until you edit them yourself;
Prowlarr has no inactive seed time. Not every client can hold every limit per
torrent, and a limit the client cannot take is skipped (with a debug log line)
rather than approximated:

| Client | Seed ratio | Seed time | Inactive seed time |
|---|---|---|---|
| qBittorrent | yes | yes | yes, 4.6 or later |
| Transmission | yes | no | yes |
| Deluge | yes | no | no |
| rTorrent | no | no | no |

Usenet clients have no seeding, so the limits do nothing there.

**Import.** When the client reports the job complete, Bindery matches it to
the book, places the file per your import mode and naming template, and marks
the book **In Library**. Ebooks land under the author's root folder (falling
back to the default root folder, then `BINDERY_LIBRARY_DIR`); audiobooks have
their own chain (the author's audiobook root folder, then the default audiobook
root folder, then `BINDERY_AUDIOBOOK_DIR`). Both defaults are set under
Settings > Root Folders, and the Add Author dialog preselects them.
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

**The file's language is checked at import.** The language filter at grab
time can only read the release name, so a release that does not say its
language gets through it. When the download arrives, Bindery reads the
language the ebook declares (an EPUB's `dc:language`, or the language a MOBI,
AZW or AZW3 file records in its header) and compares it with the
languages the book may be in: the allowed languages on the author's metadata
profile, or, when that profile allows any language, the preferred search
language if it is set to English. That is the same rule the release name was
held to, so a file is never judged more strictly than its name would have
been.

- **The file is in an allowed language**, declares no language, or declares
  one that is not a specific language (`und`, `mul`): it imports as normal.
  Every language an EPUB declares counts, so a bilingual edition tagged
  French and English is allowed under an English profile. Region tags are
  ignored, so `en-US` and `en_GB` count as English, Bokmål and Nynorsk (`nb`,
  `nob`, `nn`, `nno`) count as Norwegian, and `cmn` and `yue` count as
  Chinese. Every two letter ISO 639-1 code is recognised, including the
  withdrawn `iw`, `in` and `ji`, which count as Hebrew, Indonesian and
  Yiddish.
- **The release holds EPUBs in both allowed and disallowed languages**: the
  allowed ones are imported and the others are left out, the same way a
  disallowed format inside a release is left out. An EPUB that declares no
  language, or `und` or `mul`, is imported alongside them. Next to a
  disallowed EPUB with nothing in an allowed language, though, it does not
  save the release: it is most likely the same edition with its metadata
  stripped, so the release is rejected as below.
- **Every file is in a language the profile does not allow**: the release is
  treated as the wrong release. Nothing is placed, the Queue row is blocked
  with a message naming both languages ("file declares Swedish (swe), but the
  metadata profile "Standard" allows only English (eng)"), the release is
  added to the Blocklist so the next search picks a different one, and the
  book stays Wanted. The failure is in History and is sent to your webhooks
  like any other failed import.
- **The profile allows any language** (and the preferred search language is
  not English): nothing is rejected. When the file's language differs from
  the book's, the book is relabelled to the file's language and History
  records a "language corrected" row, because the file on disk is the edition
  you actually have.

To keep a rejected file anyway, use **Match to book** on the Queue row, or
import it through **Import → From a folder**. A manual import is your own
choice and is never refused for its language. If you locked a book's language
by editing it, a file in that language is accepted for that book even when the
profile does not allow it. Only downloads are checked: a library scan,
adopting files already in your library, and books imported before this check
existed are left alone. With the import mode set to **External**, Bindery
hands the download to your other tool without opening its files, so nothing
is checked there either. Audiobooks are not checked for language.

**The file's own title and author are checked at import (ebooks).** The release
name can say one book while the EPUB inside is another, and without a check that
file imports under the wrong book and the book is never searched for again. When
a download tied to a wanted book finishes, Bindery reads the EPUB's `dc:title`
and `dc:creator` and compares them with the book. Only a definite mismatch
counts: the title does not match **and** the author does not match, or the
title is another book by the same author (see below).

A title matches when, after case, accents, punctuation, a leading article and
`&` against `and` are set aside, it equals the book's title or one of its
variants: the title with a trailing series suffix such as `, Book 11` or
`: Alex Cross, Book 11` removed, the main title before a `: subtitle`, and the
subtitle itself when it is at least two meaningful words and does not end in a
generic word such as novel or adventure (so `The Final Empire` matches `Mistborn:
The Final Empire`). It also matches when the book's title sits inside the file's
title as whole words (`Mary, Mary` inside `Mary, Mary: Alex Cross, Book 11`), and,
for ebooks, when a file title of two or more words sits inside the book's title.
A few spellings are treated as equal: `Dr`, `Mr`, `Mrs` and `St` against Doctor,
Mister, Missus and Saint, hyphens and apostrophes left out (`Hitch-Hiker's` and
`Hitchhiker's`), and spelled numbers against digits (`Slaughterhouse Five` and
`Slaughterhouse 5`, `Twenty Thousand Leagues` and `20,000 Leagues`). Bindery does
not try to equate `Philosopher's Stone` with `Sorcerer's Stone` or `1984` with
`Nineteen Eighty-Four`.

An author matches when the file's `dc:creator` names the book's author or one of
its known aliases. A pen name or house name works through the alias list, so
"Franklin W. Dixon" counts as Leslie McFarlane once that alias is on the author.
A creator that names nobody is read as no creator at all, so it can never
disagree: `Unknown`, `Anonymous`, `Various`, `calibre`, `Project Gutenberg`, a
publisher such as `Penguin Group` or `Scholastic Inc.`, or anything containing an
`@`. A file with no title or no author never counts as a mismatch, and neither
does a comparison across scripts: if one side has no Latin letters and the other
does (a Russian, Japanese, Hebrew, Arabic or Chinese edition of a book the
catalogue holds in Latin letters), that title or author cannot disagree. Two
non-Latin texts are still compared with each other. A file whose title differs but
whose author agrees is imported, with one exception: when the file's title is
clearly a different work than the wanted book and is also the title of another
book in that author's catalogue in Bindery. Take the wanted `Hawk` and a file
titled `City of the Dead: A Maximum Ride Novel` by `James Patterson, Mindy
McGinnis`, with `City of the Dead` among the author's books. The file's creator
must name the author, so a missing or placeholder creator never triggers this.

How Bindery treats it depends on how sure it is. When the other book sits at a
different position in a series the wanted book is also in (`Hawk` is book 1 and
`City of the Dead` book 2), the file is a mismatch like any other, and `block`
mode refuses it. When nothing but the titles says they are different books, the
catalogue may simply hold two editions of one work as separate rows (a book under
its UK and its US title), so Bindery only notes it: the `contentCheck` object
carries `uncertain: true`, the file imports, and `block` mode does not refuse it.
Two rows that share an id (an ASIN, a provider id), name each other as the
original title or are at the same series position are
treated as editions and say nothing. A translated or alternate title that the
catalogue does not hold as a separate book, a subtitle, a series prefix or
suffix and a numbered volume of the same work are not mismatches either.

Positions compare as numbers, so `1`, `1.0` and `01` are one position, and a
position that is a range (`1-3`, an omnibus) or not a number counts as unknown,
never as different. A release is only noted rather than refused when every
mismatched file is uncertain; one definite mismatch makes the release definite.
The residual risk is bad provider data: if a provider gives two editions of one
book different positions in one series, Bindery reads them as two books and
`block` mode can refuse the right file. **Match to book** keeps it.

One limit: a sequel whose title extends the wanted one (`Dune Messiah` for
`Dune`) is not detected from the title alone, because the wanted title sits
inside it as whole words and counts as a match.

A wanted box set or omnibus is not compared. If a release holds several EPUBs, it is flagged
only when every one that could be read is a definite mismatch. A release with more
than 20 EPUBs is not judged (a verdict on a prefix could condemn the right book),
and the `contentCheck` note says `truncated`. The object lists at most 10
mismatches and says how many more there were.

What happens next is set by **Check ebook contents** under Settings, General,
File Naming (`import.ebook.content_check`, admin only):

- **Warn** (the default): the file imports as before, and the History row and the
  `bookImported` webhook carry a `contentCheck` object with the file's title and
  author and the book they were compared with. The History row shows it under
  the title.
- **Block**: the release is refused the way a wrong language is. Nothing is
  placed, the Queue row is blocked with a message naming what the file says and
  what was wanted, the release is added to the Blocklist and the book stays
  Wanted. **Match to book** on the Queue row, or **Import, From a folder**, keeps
  the file anyway and skips the check; **Retry import** runs it again.
- **Off**: the check does not run and no file is opened for it.

Block is opt in because a wrongly refused download costs more than a missed
mismatch. The check only runs for downloads Bindery grabbed for a book. A library
scan, manual import, **Match to book** and a download matched to its book from
the file's own metadata are never checked, and only EPUB and Kindle files (MOBI,
AZW, AZW3) are read. Bindery does not
read the book's text and does not write anything into the file.

**The files' audio tags are checked at import (audiobooks).** An audiobook
download is imported whole under the book it was grabbed for, so a release named
for the right book that also holds the tracks of other books (the next volume,
or another title by the same author) used to import all of them. When the
download finishes, Bindery reads the tags of its audio files, once, and works
out which book each file says it belongs to: its album tag, or, only when a file
has no album, its track title, and then only when at least two files share it,
so unique chapter names are never taken for other books. Titles such as
`Chapter 12`, `Track 03`, a bare number, `Prologue`, `Credits`, and promo
material (`Excerpt from ...`, preview, sample, bonus, interview, `Also by`,
teaser, trailer) say nothing about the book and are ignored, and so is a file
with no usable title. An album that is a placeholder (`Unknown Album`, `Unknown`,
`Untitled`), a publisher credit (`Audible`, `Audible Studios`, `Audiobook`,
`Blackstone Audio` and a few others), a web address or domain name, the file's own
artist, album artist or composer tag (a narrator filed as the album), or written
in a script with no Latin letters when the book's title has Latin letters, is
also no usable title.

A file supports the book when its title matches the book's title the way an
ebook's does (see above: series suffix, subtitle, a subtitle of two or more words
that is not a generic label, `(Volume 1)` style qualifier, leading article, `&`
against `and`, case, accents, `Dr` against `Doctor`, spelled numbers against
digits) or contains it as whole words, so a short title like `Dune` is supported
by `Dune Messiah`. For `Mistborn: The Final Empire` both `Mistborn: The Final
Empire` and `The Final Empire` support the book. A file that carries another
volume of the same series (`NYPD Red 4` for `NYPD Red 3`) does not.

The download is flagged when either of these holds:

- at least two files do not support the book, those files are at least 5 percent
  of the tracks read, and either at least one file does support it or at least
  two different other titles appear;
- at least two files carry another volume of the book's own series, and they are
  at least 5 percent of the tracks read.

The 5 percent floor means two bonus files or two series intro files in a folder of
sixty do not flag it. A download where every file carries one other title is not
flagged, because that is what a translated or retitled edition looks like; the
`contentCheck` object then carries `retitled` with the title and the number of
files, in `warn` and in `block` mode alike, and History shows a single quiet line.
Nothing is blocked and no message changes. A wanted box set, omnibus, anthology,
trilogy, duology, quartet, series or saga title, `Short Stories`, `Collected ...`,
`Complete Works` or `Complete Chronicles` style titles, `The Complete` plus two or
three words (`The Complete Sherlock Holmes`), `X Y Collection` (`Harry Potter
Collection`), `Treasury of Fairy Tales`, `The Best of <the author's name>` (`The Best
of Poe` by Edgar Allan Poe), a counted set such as `Four Novels` and a numeric range
such as `Harry Potter 1-7` is not compared. Ordinary titles that merely contain the
words complete, collection or best of (`The Complete Maus`, `The Best of Me`, `A
Collection of Curses`) are compared. At most 500 files are read, spread across the folder, and the whole
check has a 30 second budget: if reading runs out of time the check fails open,
the audiobook imports, and the `contentCheck` object says `truncated: true`.

The setting is **Check audiobook contents** next to the ebook one
(`import.audiobook.content_check`, admin only) and works the same way:

- **Warn** (the default): the audiobook imports as before, and the History row
  and the `bookImported` webhook carry a `contentCheck` object with the number
  of tracks read, how many support the book, and the other books found with how
  many files each (up to ten).
- **Block**: the download is blocked with a message such as `the files contain
  Corralled (9), Coming Home to Myself (8), but the book is "Chase" (2 of 19
  tracks match)`, the release is blocklisted, nothing is attached and the book
  stays Wanted. **Match to book** or manual import keeps the folder anyway and
  skips the check, and **Retry import** runs it again.
- **Off**: no tag is read.

Like the ebook check it applies only to downloads grabbed for a book, never to
a library scan, manual import or **Match to book**, and it does not change the
files or their tags.

If the library app downstream reads sidecar metadata, turn on **Write a
metadata.opf sidecar** in Settings → General (off by default). Bindery then
writes a Calibre style `metadata.opf` next to each imported ebook and
audiobook with its own title, author, series and identifiers, and refreshes it
on Reorganize. The book file itself is never modified.

**Queue and History.** The Queue page shows live downloads and, importantly,
the recovery actions: **Retry import** (after fixing a path remap), **Retry
download** on a row that failed before its files arrived (this re-sends the same
release, it does not search for a different one), **Match to
book** (attach a failed import to the right book and import it from disk), and
per-row error detail. An `imported` row leaves the Queue after 7 days (it stays on History), failed and blocked rows stay until you deal with them, and Bindery deletes finished download rows older than `BINDERY_DOWNLOAD_RETENTION_DAYS` (90 by default, `0` keeps them). Once a row is trimmed, Bindery no longer remembers that exact release was grabbed, so if you later delete the file and the book is wanted again, the same release can be grabbed again. Tick rows for **Retry selected**, or use **Retry all
failed** to cover every failed row at once. If a download client does not answer
in time the page says so above the list, so a short Queue is never mistaken for
lost downloads. History records every grab/import/failure and can
blocklist a bad release in one click. A release NZBGet or SABnzbd fails as
broken (missing articles, a failed repair or unpack, a corrupt archive) is
blocklisted for you, with the client's verdict as the reason, so the next
search picks a different one. Blocked releases are listed under
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
- **Fix match corrects the link and leaves the file alone by default.** When a
  book page shows the wrong file, the **Fix match** button reassigns it to the
  book you pick. After you pick the book, the modal asks what should happen to
  the file. **Correct the match only**, the default, keeps the file where it
  is under its current name and only changes which book it belongs to, the way
  Readarr's fix match does (#2055). **Also move and rename the file** runs the
  full import instead, so the file is moved into the target book's folder and
  renamed from your naming template, replacing your own layout for that file.
  Choosing it shows the exact destination path before you confirm; the move
  then runs in the background and Bindery cannot undo it for you. Nothing
  happens until you confirm either way. History records the change as **File
  Moved**, naming the book the file came from.
- **An import never takes a file another book already tracks.** If a download
  or a manual import lands on a path that a different book already has, the
  import stops with **Import Blocked** and the Queue row names that book and
  its id. Nothing is recorded or overwritten, and the book you imported for
  stays Wanted. If the file really belongs to the book you imported it for,
  open the book it is attached to and use **Fix match** to move it (#2937).
  When the book holding the path has since been deleted, a download, a manual
  import, adopting the file on the Import page, or adding its author takes it
  over automatically.
- A folder holding both an ebook and an audiobook for the same book attaches
  both in a single scan — one file per format, so a second scan is not needed.
- In a **flat** layout, where audiobooks sit straight in the author folder as
  `Author/Title.mp3`, each file is matched on its own. A tracked audiobook only
  claims the other files in that folder that are tracks of it (the same name
  up to a part, track, chapter or disc number, written as digits or as a word
  like `Part Two`, or a name that starts with its number such as `02.mp3` or
  `02 - Chapter Two.mp3`; a chapter title after the number is ignored), so
  another audiobook by the same author beside it is attached to its own book
  or listed as unmatched (#1985). Titles that differ only by a number, such as
  `Saga 1.mp3` and `Saga 2.mp3`, still read as tracks of one audiobook; give
  each book its own folder to keep them apart.
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
- That check never takes **another volume of the same series** as the book you
  are adding. Volume numbers are compared whether they sit in the title or only
  in the book folder, so in a `Defiance of the Fall 01/…_B094JZMCJX_….m4b`
  layout, adding volume 17 no longer attaches volume 1's file and skips the
  search (#2810). A file that already belongs to another book is never
  attached to the new one either; the new book stays wanted and is searched.
- The library scan follows the same volume rule. An untracked `Defiance of
  the Fall 01` folder is never attached to a wanted volume 17, however alike
  the titles look; it goes to volume 1 if that book is in your library, and
  otherwise waits on **Import → In your library**, where volume 17 is not
  offered as its suggestion either (#2860).
- An exact title is attached in preference to a shorter partial one, and two
  near-tied non-exact titles are left for you to choose (#2941).
- A file is never attached to a book that is a different work with a similar
  title. Titles in one series share most of their words, so the similarity
  score alone cannot tell `Harry Potter en de Relieken van de Dood` from
  `Harry Potter en de Orde van de Feniks`. After the best title is chosen, the
  scan sets aside the words both titles share and compares what is left. When
  each title has words the other lacks and none of them is close (`relieken
  dood` against `orde feniks`), the file is left unmatched for **Import → In
  your library**, even when the book it really belongs to is excluded. A
  book that close in title is not offered as the file's suggestion either, so
  Confirm never points at it. Loose, low scoring guesses are still listed for
  you to judge. A title with a subtitle added or dropped, a spelling or accent variant
  (`Æsop` and `Aesop`), a series note in brackets, an abbreviation (`Dr.` and
  `Doctor`, `Mr.`, `Mrs.`, `St.`, `Jr.`, `vs`) and the Philosopher's Stone and
  Sorcerer's Stone pair are still the same work. Other alternate titles of one
  book are not recognised and wait on the Import page. Adding a book runs the
  same check before it decides you already own the book (#3388). This check
  only adds rejections: it never makes a file match that did not before, and it
  does not stop a title that contains the other, such as `Dune` and `Dune
  Messiah`, from matching as it always did. It can also leave a genuine match
  unmatched, for example when the file name carries a word the catalogue title
  does not, such as the author's name, so that file waits on the Import page to
  be confirmed.

## Adopting files already in your library

A library scan attaches every file it can match with confidence and leaves
the rest for you. Those books wait on **Import → In your library**, the page
`/import` opens on. Each row is one **book**, not one file: a 193 track
audiobook folder is one row, a folder whose audio subfolders are all discs
(`CD1`, `Disc 2`, `Disk 3`, or the book's own title in front, as CD rips name
them: `Fjellvinden (2002)/Fjellvinden Disc 1`) is one row named after it, and `Dune.epub` beside
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
- Suggestions come from **every book by the author the scan matched**,
  whatever its status, and each shows that status. A book you skipped or one
  that is already **Imported** is never a one click Confirm; adopting into a
  book that already has a file adds the new file alongside it (#2879).
  Suggestions are worked out by the scan, so a book added since the last scan
  appears after the next one; until then, search for it in the editor.
- **Files that name another author than their folder** say so: the row reads,
  for example, **Files say Katy Evans, folder says James Patterson**. The
  author comes from the audio tags, or from track names that all read
  `Author - Title 1`, `Author - Title 2` and so on. Artist, Album Artist and
  Composer are all checked, so a narrator in Artist beside the author in Album
  Artist is not a conflict, and neither are credits such as Various Artists,
  Unknown Author(s), Anonymous, Full Cast, or an audiobook publisher such as
  Brilliance Audio, Recorded Books or Tantor Audio, however they are
  punctuated. Such files are also never attached by the scan to the
  book their folder is named after. The row is named after the
  book the files name (the album tag, or the title in those track names), and
  suggestions come from the files' author first, scored on that title. Books
  by the folder's author are still listed below them, marked **Folder author
  only**, but they are never preselected and never a one click Confirm. When
  the files' author is not in your library, the row offers **Add author** for
  them, not for the folder's author (#2942).
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
  moved to another book stays with that book. If the files were adopted into a
  book that was Skipped, undoing the adoption restores it as Skipped (including
  when multiple adoptions were made into the same Skipped book). A book or author the adoption
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
- **Too small to be a book**: an ebook format file under 4 KiB (a notes,
  readme or link file, about two pages of plain text at most) is never
  attached to a book automatically: the library scan does not match it to a
  book, and adding an author does not treat it as a book you already own. It
  is listed here on a row of its own with that label and no suggestion, and
  cannot be adopted. Ignore it, or delete it from the folder. A notes file
  beside the book file that matched is still counted quietly as that book's
  companion and not listed. Audiobook tracks are not judged by size, and
  files already tracked are not affected (#2944). The same floor applies to
  the files an automatic import takes from a download (#3224). If a file that small really
  is the book, **Manual Import** is the override: it imports whatever file
  you pick, whatever its size.
- **In your audiobooks folder** / **In your ebooks folder**: when
  `BINDERY_AUDIOBOOK_DIR` is its own folder, a row found in the other
  format's folder (say an `.epub` under the audiobooks root) says so, and its
  suggestion is never a one click Confirm. You can still adopt it after
  checking. With one combined folder for both there is nothing to label.
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
- It lists one row per **book**, the same idea as the list above. A folder of
  audio is one row, and so is a folder whose audio subfolders are all pieces of
  one recording (`CD1`, `Disc 2`, `Disk 3`, `Part 4`, `Chapter 5`, or the
  book's own title followed by a CD or disc number, `Fjellvinden Disc 1`). Subfolders
  named `Book 1`, `Vol 2` or a bare `1` are separate books, because a series
  stored that way is several books rather than one long one, so each gets its
  own row and its own match.
- Point it at an audiobook's own folder and every track is a row, because the
  folder you scan can just as well be a downloads folder of separate one file
  audiobooks. Pick the book on one track and press **Use this book for the
  other tracks in this folder** to give every track in that folder the same
  book. Tracks imported together for one book land in one audiobook folder,
  named from your audiobook file template, the same as a downloaded multi part
  audiobook. Importing an audiobook onto a book that already has one is
  refused rather than split into a second folder: the files stay where they
  were and the queue says why. Delete the existing audiobook first to replace
  it.

## Restyling files you already have

Your naming template only applies to files Bindery places itself, so changing
it leaves everything already on disk where it was. **Rename files** is the
catch-up: it recomputes where every file Bindery tracks should live under the
current template and moves it there. It reaches the same renamer the import
path uses, so a reorganized library and a freshly imported one come out
identical.

Nothing moves until you say so. The preview lists every tracked file with its
current path, the path the template computes, and why it will or will not move:
**Will move**, **Already correct**, **Destination exists** (something else is
already there, so it is skipped rather than overwritten), **Not on disk**, or
**Error**. Pressing the button applies only the clean moves, and it recomputes
each destination on the server rather than trusting the list you were shown, so
a file that changed under you is skipped instead of moved somewhere stale. A
move is always a move, never a copy, so hardlinks survive and nothing is
duplicated. Bindery also prunes the folder a file left behind when it empties,
and refreshes the `metadata.opf` sidecar when you have that turned on.

The file extension comes from the file's current name. If a file has lost its
extension (for example from a template with `{Title}{ext}` and no dot), Bindery
takes it back from the file's rename history, or failing that from what is
inside the file (epub, mobi, pdf and similar). History is preferred, except
that a file whose contents are clearly a different format (a PDF, say) gets the
extension of its contents. Content sniffing cannot tell mobi, azw3 and prc
apart, so a file recovered that way gets `.mobi`, which Calibre and Kindle tools
read by content. Fix the template to
`{Title}.{ext}` and run Rename files again to repair those files. A file whose
extension cannot be worked out is shown as **Error** and left alone, and so is
any move where the template would produce a name with no extension. Put a dot
before `{ext}` in the ebook template, and leave `{ext}` out of the audiobook
folder template, which names a folder and has no extension to fill in. Saving
an audiobook template with `{ext}` is refused.

Three scopes, all the same preview:

| Scope | Where |
|---|---|
| One book | the book page, **More** → Rename files |
| One author | the author page, **More** → Rename files |
| Your whole library | Settings → General → Library → **Reorganize Library** (#2296) |

**Run Scan library first.** Reorganize only knows about files already attached
to a book, so anything sitting in your library that no scan has matched is
invisible to it and stays where it is. That is why the library scoped button
lives directly below the scan button rather than beside it. It is admin only,
like everything else on that tab that names server paths.

## Metadata: where book data comes from

- **OpenLibrary** is the default primary provider — it decides what an
  author's catalogue looks like. It is community data: expect occasional
  duplicates, language mix-ups, and box-set entries. The "primary" selector
  (Settings → Metadata Profiles → Library Defaults) offers OpenLibrary,
  **DNB** (German National Library), **Nasjonalbiblioteket** (National Library
  of Norway: Norwegian books under their original titles, only used when chosen
  as primary), **BnF** (Bibliothèque nationale de France: French editions under
  their French titles, translations included, only used when chosen as primary),
  and **Hardcover**. Switching the primary away from Nasjonalbiblioteket or BnF
  switches it off entirely, so authors linked to it stop syncing until you
  relink each one with "Link metadata" on their page.
- **BnF** suits a library of French editions whose indexers name releases
  after the French title ("Alice au pays des merveilles", not "Alice's
  Adventures in Wonderland"). Its data is under the Licence Ouverte / Open
  Licence 2.0, credited in Settings where you choose it, and each BnF book and
  author page shows the credit with the date its record was retrieved;
  Bindery fetches no covers from BnF, so covers come from the other
  providers. BnF is moving its catalogue to a new record model until the end
  of 2026, and records created since June 2026 are not yet linked to their
  author, so Bindery finds those by the author's name, only when no other
  person in BnF's authority file has the same name, and treats an author's
  BnF catalogue as possibly
  incomplete: catalogue reconciliation never offers to remove a book because
  BnF did not list it. BnF publishes no rate limit, so Bindery sends it at most
  one request a second; refreshing an author with a long catalogue can take
  around fifteen seconds.
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

  When a metadata profile restricts languages, Bindery checks Hardcover's
  editions for each author work in one batched request. This happens whether
  Hardcover is the primary provider or supplements OpenLibrary, so the
  Hardcover works merged into an OpenLibrary author are checked too. A
  translated default edition is not treated as the language of the whole work:
  any edition in an allowed language keeps the work, while a work is rejected
  as non-allowed only when the lookup completes and finds no allowed edition
  and some edition, default or not, records another language. A work with no
  language on any edition stays unknown. This filtering evidence does not
  rewrite the displayed language, which remains the
  provider's preferred/default language or the user's locked value. If the
  evidence is indeterminate or its lookup fails, normal refreshes fall through
  to the existing edition-sampled, author-majority, and scalar language before
  applying **When book language is unknown**. The author-majority fallback is
  not applied to a work whose title is written in a different script from the
  author's other titles (a Cyrillic title in an English author's catalogue, for
  example), so such a work stays unknown. OpenLibrary edition sampling only
  runs for OpenLibrary works, and takes the language and a missing cover from
  the edition OpenLibrary features on the work's own page first, falling back
  to a small sample of its editions (and a cover in the sampled language) only
  for what that edition lacks. **Reconcile catalogue** treats a
  failed lookup as indeterminate rather than offering the row for removal.
- **Covers from your own files.** When no metadata provider has a cover for
  a book, Bindery uses the art embedded in the file you import (M4B, MP3,
  FLAC, EPUB, and the cover a MOBI, AZW or AZW3 file marks as its cover), or
  a `cover.jpg` / `folder.jpg` in a multi-file audiobook's folder. Books already in your library pick theirs up on the next library
  scan. A provider's cover is never replaced, so this only fills blanks.
- **Your own cover.** **Upload cover** (or **Change cover**) under a book's
  cover sets it from a JPEG, PNG, WebP or GIF image of up to 10 MB. It
  replaces whatever cover the book had, and a refresh keeps it, since
  refresh only fills an empty cover.
- **Google Books** (free API key) and **Audnexus/Audible** (audiobook
  narrator, duration, by ASIN) enrich further.

For a Hardcover audiobook, the chosen audio edition can fill a missing book
duration before indexer search. Audnex may update that duration when an ASIN is
available. Explicit audio formats take priority over an unknown format with a
runtime; runtime breaks ties between equally ranked editions. A runtime alone
does not turn a known print format into an audiobook. Edition hydration respects
a manually locked language, including a language deliberately cleared to empty.
If a pending metadata update conflicts with a book edit made during fetching,
the newer edit wins; a later refresh can fill remaining gaps. This includes
language detected from an imported EPUB. Hydration uses the current monitoring
and ASIN values before searching and retains provider series links during ingestion.
This refresh also runs when the edition fetch is empty, unchanged, or fails.
If a book is rebound while editions are being fetched, later edition writes for
the old book identity are skipped.

Audnex enrichment follows the same rule. When an Audiobookshelf import or a
Hardcover list sync enriches a book you edited while the Audnex lookup was in
flight, your edit is kept and that enrichment is skipped. The **Enrich** button
on the book page retries once on top of your edit; if the book keeps changing
it asks you to try again. **Rebind** does the same when the book changes while
the new record is being fetched.

The same goes for the other writes that follow a provider lookup. When an
Audiobookshelf import merges a book or an author with its upstream record and
you edit it during the lookup, the merge is applied on top of your edit; if
the row keeps changing, the merge waits for the next import. That protects the
fields the merge does not take from upstream, such as monitoring, the narrator,
the format and the quality, metadata and root folder profiles. The fields it
does take from upstream follow the usual metadata conflict rules even when you
just changed them: for a book the description, cover, original title, release
date, language and ratings, and for an author the name, description, image,
disambiguation and sort name. A **Refresh Metadata** on
an author re-reads each book it matched by title just before updating it, so
edits made while the refresh runs are kept. The scheduled author metadata
refresh skips an author you edited while its profile was being fetched and
picks it up on the next run.

Which of those a given book actually came from is on the book page, under
**Metadata source**. It names the provider, shows the identifier the book is
bound to with a copy button, and lists any other provider ids the same book is
known by. That is the thing to check before deciding a book needs re-binding,
and the id is what to quote in a bug report. Hover or activate **Links** while
confirming a book in the Add to library dialog or in the book header to open
trustworthy upstream pages for OpenLibrary, Google Books, Hardcover, DNB,
Nasjonalbiblioteket and BnF records.
Calibre and Audiobookshelf ids remain visible only under **Metadata source**
because they do not map to stable public pages.

When metadata is wrong, you have three levels of fix:

1. **Edit metadata** on the book — edited fields are **locked** so refreshes
   never overwrite them ([guide](Metadata-Editing-Wiki.md)).
2. **Re-bind** the book, or **relink** the author ("Find better match"), to a
   different provider record when the match itself is wrong.
3. A **metadata profile** (languages, minimum page count, minimum edition
   count, skip part books) filters what a catalogue sync lets in, and what
   **Fill gaps** or **add all** on a series creates
   ([#2208](https://github.com/vavallee/bindery/issues/2208)). Adding a single
   row from a series is an explicit pick and is not filtered. Filters screen
   books as they arrive; **Reconcile catalogue** on an author applies them to
   books already stored.

Box sets need no setting. A work whose title plainly names a bundle ("... Box
Set", "3 Books Set", a title ending in "(Set)" such as "Just Beyond (Set)",
"Carton of 10 Signed Copies") is dropped from every author catalogue as it is
fetched, on any provider. **Skip part books** in the metadata profile adds the
shapes that are a judgement call, and is off by default because each of them has
real single books it would wrongly catch: a title ending in "Omnibus",
slash-separated titles like "Title A / Title B", "Books 1-3", a parenthesised
series range such as "Harry Potter (series) 1-3", and a count of books with no
"set" word, either at the end of the title ("... - 10 Books") or as a retailer
listing ("2 Daniel Silva Books!"). A real single title that ends in a count,
such as "Top 100 Books", is caught too. A title that merely starts with a
number, like "10 Books That Changed America", is not touched. Neither filter
touches "Trilogy". A bundle already in your library is left alone; it just stops
being offered back by the catalogue.

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
upstream shows up on the first click. The Refresh button is freed after a
minute; a refresh that takes longer carries on in the background, and the page
keeps watching it and shows the result when it ends. The same goes for the
sync that adding an author starts: until it ends, the author page says the
catalogue is being fetched rather than that the author has no books, which for
a large catalogue can take a few minutes. Clicking Refresh again while a refresh for that author is still
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
- **When a provider struggles:** every metadata provider waits and retries
  when it is told to slow down, honouring the provider's own `Retry-After`,
  and holds its other requests for that long too. When a provider still
  refuses with a rate limit after that, the pass stops and the remaining
  authors wait for the next hour. When three authors in a row fail because the provider is down (server
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
not remove their `not_in_current_catalogue` rows. With a complete catalogue,
an absent row from that same provider may be actionable. A row from a different
provider is kept as indeterminate when Bindery cannot correlate it to the
current catalogue; changing providers alone is not evidence that a work is
obsolete. A correlated work that the metadata profile explicitly rejects can
still be removed for that rejection reason. The preview lists each
indeterminate row separately with its provider and the incomplete-evidence
reason so it can be reviewed manually; these informational rows have no
selection control and cannot be sent for removal.

### Reviewing duplicate titles

**Duplicate titles.** The same book often reaches the catalogue twice under
slightly different titles — "The Martian" and "Martian", "Dune" and "Dune
(Unabridged)". Open the author and choose **More → Review duplicates…** to see
groups of titles that look like the same book. Each group shows which rule
matched (identical after normalisation, a leading article dropped or filed behind
a comma as in "Trace of Death, A", an edition
marker dropped, or one title being the main title or subtitle of the other),
and each row shows the rules that pulled it in. A main title or subtitle match
only counts at a colon, bracket or spaced dash, so Asimov's "Foundation" is not
flagged against "Foundation and Empire", and it is skipped when the two books
are known, different entries in the same series ("Mistborn" against "Mistborn:
The Well of Ascension"). Nothing is changed automatically: the only action is
**Exclude** on a row you judge to be the duplicate, which marks it excluded
without deleting anything. An excluded row stays in its group, struck through,
with an **Include** button to undo it; a group leaves the report once fewer
than two of its rows are still included.

**The evidence on each row.** Under every title the review shows what Bindery
already knows about that row, so you can decide without opening each book:
whether it has files (ebook, audiobook, and the format, such as epub or m4b),
its status (Wanted, In Library, Skipped), release year, language, ISBN, ASIN,
and series with position. The rows are numbered, and above them the group
lists what the rows agree or disagree on:

| Marker | What it means |
|--------|---------------|
| Rows share an ISBN or ASIN | strong evidence they are one book |
| Rows hold the same series position | strong evidence they are one book |
| Rows hold different positions in one series | probably different books of that series |
| Release years more than a year apart | possibly a different book, or an old and a new edition |
| Different languages | possibly a translation rather than a duplicate |

Languages are compared by meaning, so "en", "eng" and "English" agree, and
series positions by number, so "1" and "1.0" agree. An unknown year, language
(including "und" and "mul") or position never counts as a disagreement.

**Which row to keep.** When exactly one row in a group has files, it is marked
**Keep: has files**. If the rows also have no disagreement, and something
positively ties each empty row to that one (a shared ISBN or ASIN, the same
series position, or a title that is the same apart from punctuation, a leading
article or an edition marker), the group offers **Exclude the empty rows in
this group**, which shows you the rows it will exclude and asks you to confirm
before it does anything. A title that only contains the other one, such as
"Mistborn" and "Mistborn: The Hero of Ages", is not enough on its own. It is
never offered when the rows disagree, or when more than one row has files,
because then the files alone do not tell you which row is the real book. A row
with files is never part of that suggestion, and if one of the empty rows is
imported after you opened the page, Bindery skips it and tells you. You can
still exclude a row with files with its own **Exclude** button, and Bindery
asks first. Excluding never deletes a file.

**Across the whole library.** On the **Books** page, **Review duplicates**
opens a page listing the duplicate groups for every author, a page at a time,
with the same evidence and the same buttons. It finds exactly the groups each
author's own window would: titles are only compared with other titles by the
same author. After you exclude rows, a group that no longer has two included
rows drops off the list. On a shared install with per user libraries, each
person sees only the authors they could open themselves.

A title filed with its article last ("Trace of Death, A") counts as the same
title as "A Trace of Death", both here and when a refresh decides whether a
book is already in your library, so a refresh no longer adds a second row for
it. Pairs that already exist are never merged for you. They stay as two rows
and appear here as a group, and you choose which to exclude.

## How author names are filed

One value decides the order of the Authors list, the order of the OPDS author
feed, and what the `{SortAuthor}` naming token writes: the author's **sort
name**, which is the display name rewritten as "Last, First". Bindery derives
it when the author is created, and a metadata refresh replaces it with the
provider's own sort name whenever the provider supplies one.

So the Authors page is sorted by last name out of the box. **Last name A to Z**
in the Sort menu gives you Asimov, Atwood, Bardugo; the two **First name**
entries are the ones that file Isaac under I (#2805). The merge authors dialog
lists authors in the same last name order, and the Books grid's sort menu offers
**Author A to Z** and **Z to A**, which only the table's column header reached
before.

The Books column of the Authors table reads *have / total*, for example 5 / 12,
and each card in the grid says how many of the author's books are in your library,
by the In Library rule under rule 2 near the top of this page (#3092). An author with no books shows a dash.
On an author's own page the book list has a **Sort** control (title, published,
added, each both ways) and Monitored and Language filters beside Type, Status and
Published (#3087); books with no release date sort last.

| Display name | Files under | Rule |
|---|---|---|
| Isaac Asimov | Asimov, Isaac | the last word is the surname |
| Robert A. Heinlein | Heinlein, Robert A. | middle names and initials stay with the forename |
| Martin Luther King Jr. | King, Martin Luther Jr. | a generational suffix (Jr., Sr., II, III, IV) follows the forename |
| Vincent van Gogh | Gogh, Vincent van | a lowercase particle travels with the forename |
| Johann Wolfgang von Goethe | Goethe, Johann Wolfgang von | the same rule, German `von` |
| Ludwig van Beethoven | Beethoven, Ludwig van | the same rule, Dutch `van` |
| Thomas De Quincey | De Quincey, Thomas | a capitalised particle belongs to the surname |
| Dick Van Dyke | Van Dyke, Dick | the same rule, so `van` and `Van` file differently |
| Ursula K. Le Guin | Le Guin, Ursula K. | French `Le` leads whatever its case |
| Daphne du Maurier | Du Maurier, Daphne | French `Du` leads too |
| Jose de la Cruz | Cruz, Jose de la | a compound particle moves as one unit, never under L |
| Seanan McGuire | McGuire, Seanan | `Mac`, `Mc`, `O'`, `Fitz`, `St` and `Saint` are part of the surname |
| Flannery O'Connor | O'Connor, Flannery | the same rule |
| Madonna | Madonna | a single word name is left alone |
| Asimov, Isaac | Asimov, Isaac | a name that already carries a comma is left alone, because someone has already inverted it |
| 村上春樹 | 村上春樹 | a name written entirely in CJK script is already surname first |

Where the particle tables cannot decide, **case decides**: a lowercase particle
in the middle of a name travels with the forename, a capitalised one stays with
the surname. That is the BibTeX "von part" convention, and it reproduces the
Library of Congress outcome without Bindery having to know which language a
name belongs to.

It is a heuristic, and the limits are worth knowing:

- Anyone who writes their own particle against their language's convention is
  filed the other way round.
- A Chinese, Japanese or Korean name written in Latin letters is read forename
  first, because the script is the only signal available. "Cixin Liu" files
  under Liu, and the same name written "Liu Cixin" files under Cixin.
- **There is no field for it.** The sort name cannot be edited by hand, in the
  UI or through the API. An author filed under the wrong letter stays there
  until the metadata provider offers a sort name of its own, which the next
  refresh adopts.

If your naming template uses `{SortAuthor}`, the same value names the folder on
disk, so the letter an author files under and the folder it lives in always
agree.

## Finding a setting when you do not know its tab

The search box at the top of Settings matches tab names and section headings
(Telemetry, File Naming, API Keys and so on) in your language, and for admins also
the key of any registered setting. Press `/` to jump to the box, type a few
letters, then use the arrow keys and Enter, or just tap a result. Enter on its own
takes the first one, and Escape clears the box. A tab or key result opens that
tab; a heading result also scrolls to the section. The list closes when you click
away, and says when more than eight things matched.

It does not search individual field labels or help text, so a setting that has no
heading of its own is found through its tab or section. Non admins are only
offered the tabs and sections they can see: the admin sections of General, such
as File Naming and Downloads, are left out for them.

## Settings most people never touch

The curated Settings tabs hold the things nearly every install cares about. Behind
them Bindery stores its whole configuration as plain key/value rows, and
**Settings, Advanced** shows all of them: the key, what it holds, its default, the
values it accepts, and whether the change takes effect now or at the next restart.

Reach for it when a guide or an issue names a key rather than a screen, when you
want to confirm what an install has actually stored, or for the rare knob that
never earned a control of its own. Three things it will not let you do, all on
purpose:

- **Credentials are never shown.** An API key or a session secret is stored, not
  displayed, and the ones with their own screen are only editable there.
- **Rows Bindery writes for itself are read only.** Resume points, last run
  timestamps and one shot guards are shown so you can see them, and hand editing
  them corrupts whatever wrote them.
- **Keys nothing reads are labelled as such**, rather than quietly accepting a
  value that changes nothing. A key Bindery does not recognise at all is flagged
  and can be removed.

Every entry carries its own description, in English, written where the setting is
defined rather than translated per language.

## What Bindery deliberately does not do

Knowing the edges saves time:

- **Track reading.** No read/unread, progress, or ratings of your own. Bindery
  shows the community rating the metadata provider supplies (on the book page,
  in the Books and author tables and grids, and in Add to library; the Books
  table sorts by it, unrated books last), but does not record yours. Bindery acquires
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
- **Serve linked files.** Linked *folders* work for imports and downloads: a
  library root reached through a symlink or bind mount, or an author folder
  linked to another disk (`/books/Author -> /mnt/disk2/Author`), is written
  through on import and followed when serving a book, including when it
  points outside your library folders, because only you can create one.
  Deleting a book or author with its files is the exception: it only removes
  files that resolve to a place inside your library folders, so files behind
  a linked folder that points elsewhere stay on disk (the log says
  `path containment: refusing to delete file`) and the book is still removed
  from Bindery. If you link author folders to another disk, add the link
  target as an extra root folder (Settings, Root Folders), or deleting a book
  there leaves its files behind.
  Library Scan follows a library or audiobook folder that is itself a symlink
  and records the books under the path you configured, but it does not
  descend into a linked folder *inside* it, so books under a linked author
  folder are not found by a scan. A bind mount is not a link and scans
  normally. A linked *file* is
  not a book file: imports place regular files only (a symlink inside a
  download never lands in your library, and a download folder that is itself
  a symlink is refused in every import mode, including the drop folder
  handoff), Library Scan and the
  Audiobookshelf import do not attach one to a book, and downloads (browser,
  OPDS, the Calibre bridge) refuse to serve one. An audiobook zip leaves out
  any link inside the book folder, file or folder. Replace a linked file with
  the file itself. Reorganize moves your own links along unchanged.

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
moving things by hand and it never happens
([Restyling files you already have](#restyling-files-you-already-have)).
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

**An author is filed under the wrong letter.**
The Authors list sorts by sort name, the display name rewritten as "Last,
First" ([How author names are filed](#how-author-names-are-filed)). Particles
are settled by case, so a lowercase `van` files under the word after it and a
capitalised `Van` files under V. There is no field to correct it by hand; a
metadata refresh takes the provider's own sort name when it has one. The two
**First name** entries in the Sort menu order by display name instead.

**A book is on hardcover.app but doesn't show up in search.**
No Hardcover token configured — set one in Settings → API Keys.
([troubleshooting](Troubleshooting-Wiki.md))

**The torrent finished ages ago but never imported.**
Check the Queue for the error. Usual causes: category mismatch, the client
and Bindery seeing the same storage at different paths (set a **path remap**
on the download client), or files the container can't read. **Retry import**
after fixing. ([troubleshooting](Troubleshooting-Wiki.md))

**A red "Imports have a problem right now" notice shows on the Authors page.**
Bindery has imported at least one download before (a `bookImported` or
`downloadFolderImported` history event), but right now it cannot. The notice is
shown to admins only and says which area to look at:

- a folder Bindery imports into is missing or not writable: each root folder, or
  the library folder when you have no root folders (plus the audiobook folder
  when set);
- a download folder is missing or not readable. Only readability is required,
  so a read only download folder used in copy mode is fine. This check is
  skipped when a path remap is set, globally or on an enabled client, and for an
  empty download folder (the Windows default);
- no enabled indexer;
- no enabled download client;
- every enabled download client failed its last health check (unreachable, or
  its download folder is not readable).

Folders are checked without writing anything (no probe files), at most every 30
seconds. The notice never names a folder path; the Settings page it links to
does. Limits worth knowing:

- **It needs two views.** A problem has to be seen for two minutes before the
  notice appears. The page checks when it opens and again when you return to
  the tab (at most once a minute), not on a timer. So the first view after a
  restart, or the first view of a new problem, only starts the clock; the notice
  shows on a view at least two minutes later (a reload or switching back to the
  tab). If more than ten minutes pass between views the clock starts over, and a
  user who stays on one page the whole time sees nothing.
- **Client health is slow.** It is the stored result of the 15 minute periodic
  check. A client that restarted for a moment can show red for up to about 15
  minutes after the two minute wait, and a fix takes as long to clear. The
  message says the client could not be reached or its download folder is not
  readable, because the check cannot tell which.
- **A hung mount reports nothing.** If a network mount never answers its first
  check, there is no earlier result to fall back on, so nothing is reported
  until it answers.
- **One folder is enough.** A single unavailable root folder (an external drive
  you took offline on purpose) raises the notice even though imports into your
  other root folders still work. The message says one library or download folder
  is not available, not that everything is blocked.
- On Windows only the folder's existence and that it can be opened are checked,
  not whether it is writable.

Installs that never imported a download, such as ones that only scan an
existing library or use Calibre or Audiobookshelf, are not nagged.
(`GET /api/v1/system/setup-state` carries `blocked`, `blockedArea` and
`blockedReason` for admins.)

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

**Test connection says the Calibre container cannot see my library path.**
Calibre cannot open the path your **Push path remap** produces. With Calibre
on a Windows desktop, the right side of the remap must be the share address,
for example `/books:\\nas\media\books`, not a mapped drive letter: a mapped
drive belongs to one logon session and the running Calibre may not see it.
Check the share opens in Explorer on that PC
([step 1 of the Windows runbook](Calibre-Windows-Desktop-Wiki.md#1-find-the-share-address-calibre-can-open)).
For a desktop Calibre, pull mode avoids the share altogether
([Set up pull](Calibre-Windows-Desktop-Wiki.md#set-up-pull)).

**Pushing to Calibre fails with `[Errno 22] Invalid argument`.**
The path starts with `\\?\\\` and is over about 200 characters: Calibre
builds an invalid long path for a network share. Bindery Bridge 0.6.1 works
around it. Upgrade the plugin, restart Calibre, and click **Retry failed**
under **Delivery queue** on the Calibre tab
([troubleshooting](Calibre-Windows-Desktop-Wiki.md#troubleshooting)).

**Push all says a book is already in Calibre, but it has no file there.**
An earlier failed add left an empty record, and the next push matched it.
Bindery Bridge 0.6.2 removes the record when an add fails and attaches the
file on the next push. Upgrade and restart Calibre. Bindery has recorded
those books as delivered, so click **Reset delivery state** on the Calibre
tab and then run **Push all to Calibre**
([troubleshooting](Calibre-Windows-Desktop-Wiki.md#troubleshooting)).

**Calibre says `Cannot determine book format from extension` with a folder.**
Bindery recorded a folder as the book's ebook file, and Calibre cannot add a
folder. Fix the book in Bindery rather than the plugin: check its **Files**,
use **Forget this file** on the wrong entry and import the right ebook. In the
case this came from, the folder held an audiobook of a different book.

**Books imported while Calibre was closed have not reached it yet.**
They are waiting. Bindery queues every imported ebook and delivers it when
Calibre is reachable: with the push transport within about a minute of
Calibre running again, with pull at the plugin's next check, 60 seconds by
default. A book Calibre rejects is retried after 1 minute, 5 minutes, 15
minutes, 1 hour, 6 hours and then daily, and marked failed after 8 attempts.
The **Delivery queue** section of the Calibre tab shows how many are waiting,
delivered and failed, and each failed book with its error; the book's own
page shows **Waiting for Calibre**, **In Calibre** or **Calibre failed**.

**Running Push all to Calibre again does not fix failed books.**
Expected. Push all only queues books the queue does not hold yet, so it never
resends a delivered book and leaves failed ones alone. Fix the cause shown in
**Failed deliveries** on the Calibre tab, then click **Retry failed**
([the delivery queue](Calibre-Windows-Desktop-Wiki.md#watch-the-delivery-queue)).

**Calibre never picks up new books in pull mode.**
Open the plugin's **Customize** dialog in Calibre and read **Pull status**.
"Bindery is set to push" means **Transport** on the Calibre tab is still
Push. "Bindery rejected the API key" means the keys differ, and the plugin
then waits an hour; click OK in the dialog to retry at once. "Paused: a
different library is open" means Calibre has another library open than the
one pull was turned on in. On Bindery's side, the **Delivery queue** panel
says when Calibre last checked in
([pull troubleshooting](Calibre-Windows-Desktop-Wiki.md#pull-troubleshooting)).

**Every request from the Calibre plugin takes about 20 seconds.**
The plugin's **Bindery URL** says `localhost` and Bindery runs in WSL or
Docker Desktop on the same Windows PC. Windows tries IPv6 `::1` first, the
forwarding drops it silently, and the fallback to IPv4 takes about 21
seconds. Use `127.0.0.1` or the machine's address instead
([pull troubleshooting](Calibre-Windows-Desktop-Wiki.md#pull-troubleshooting)).

**Only one format of a book reached Calibre.**
Bindery sends every ebook format of a book to the same Calibre record, which
needs Bindery Bridge 0.7.0 or later. With an older plugin the first format
arrives and the rest are skipped with the reason `bridge cannot add a second
format; update the Calibre plugin to 0.7.0`. Update the plugin and restart
Calibre; Bindery then delivers the skipped formats on its own
([Calibre integration](Calibre-Integration-Wiki.md#deliveries-are-queued-and-retried)).

**Audiobooks never appear in Calibre.**
Expected. The Calibre write integration sends ebooks only, and **Push all to
Calibre** lists an audiobook with no ebook under Skipped. Use Audiobookshelf
for audiobooks
([Calibre integration](Calibre-Integration-Wiki.md#troubleshooting)).

**How do I get books onto a Kobo?**
Through Calibre: connect the Kobo by USB and use **Send to device**. Kobo
renders KEPUB better than EPUB; recent Calibre converts to it, and the
KoboTouchExtended plugin adds it if yours does not
([Windows runbook](Calibre-Windows-Desktop-Wiki.md#get-books-onto-a-kobo)).

---

More depth: [QUICKSTART.md](QUICKSTART.md) ·
[DEPLOYMENT.md](DEPLOYMENT.md) ·
[Storage & hardlinks](Storage-And-Hardlinks-Wiki.md) ·
[Troubleshooting](Troubleshooting-Wiki.md) ·
[Migrating from Readarr](Migrating-From-Readarr-Wiki.md) ·
[ABS import](ABS-Import-Wiki.md) ·
[Calibre on Windows](Calibre-Windows-Desktop-Wiki.md) ·
[Multi-user](multi-user.md)
