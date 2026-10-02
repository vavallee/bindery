# Calibre and Calibre-Web-Automated

Bindery can hand a freshly imported book to Calibre, or to Calibre-Web-Automated (CWA), in three different ways. They look alike in the settings, they are configured on two different tabs, and picking the wrong one is the most common reason a book is grabbed and imported yet never shows up in Calibre. This page tells the three apart, says what each one does on disk, and ends with the troubleshooting most people arrive here for.

## Why this page exists: Calibre only knows what is in `metadata.db`

Calibre does not watch its library folder. A file placed under `/calibre-library/Author/Title/` by hand, or by Bindery, is invisible to Calibre and to CWA until something registers it in `metadata.db`. Every way of connecting Bindery to Calibre is therefore one of two things:

- a **registration call**: Bindery tells Calibre about the file (`calibredb add`, or the Bindery Bridge plugin), and Calibre copies it into its own tree and records it in `metadata.db`
- a **copy into a watched folder**: Bindery drops a file where CWA's ingest watcher (or another tool) will find it, and that tool does the registration

"Bindery writes into the Calibre library directory" is not one of the options, and setting `BINDERY_LIBRARY_DIR` to the Calibre library does not make Calibre see the files.

## The three topologies at a glance

| Topology | Who owns the library | What Bindery does on import | Where the settings live | Copies on disk | Formats |
|---|---|---|---|---|---|
| **1. Register with Calibre** | Bindery owns its library; Calibre owns a separate one | Places the file in the Bindery library, then calls `calibredb add` or POSTs to the Bridge plugin | Settings, Calibre tab, **Write integration** (`calibre.mode`) | Two: Bindery's copy and the one Calibre makes inside its library | Ebooks only. An audiobook import does not hand anything over |
| **2. Mirror into the CWA ingest folder** | Bindery owns its library; CWA owns a separate one | Places the file in the Bindery library, then copies it into the ingest folder; CWA consumes and deletes that copy | Settings, Calibre tab, **Calibre-Web-Automated (CWA)**, Ingest folder path (`cwa.ingest_path`) | Two: Bindery's copy and the one CWA files into its library. The ingest copy is transient | Ebooks only. Audiobooks are never mirrored |
| **3. An external tool owns the library** | CWA, Calibre or Storyteller owns the only library | Does not write into the library at all; copies or hardlinks the finished download into a drop folder and waits for the tool to file it | Settings, General tab, File Naming, **Import Mode** set to External, then Drop folder, Layout, Placement | The managed copy the external tool makes, plus a drop folder copy until that tool consumes it (the default `copy` placement), on top of the download, which is never moved | Ebooks and audiobooks |

Topologies 1 and 2 are independent switches and both run in Auto, Move, Copy and Hardlink import modes. Topology 3 replaces Bindery's own import, and topologies 1 and 2 do not run in it.

## Topology 1: register with Calibre

Bindery imports the file into its own library as usual, then tells Calibre about it. Calibre does what it always does with a new book: it copies the file into its own library tree and records it in `metadata.db`. The file now exists twice, once in the Bindery library (the author's root folder, falling back to `BINDERY_LIBRARY_DIR`, or to `BINDERY_AUDIOBOOK_DIR` for audiobooks) and once under the Calibre library, and that is expected. It is what makes Calibre's own edits, conversions and deletions safe: they never touch Bindery's copy.

The `calibredb` variant needs **Library path** on the Calibre tab set to the directory that holds `metadata.db`, as seen from inside the Bindery container. The plugin variant does not: the plugin adds to whatever library its Calibre has open.

### Deliveries are queued and retried

An import does not talk to Calibre itself. It records each imported ebook file in a delivery queue and wakes a background worker, so the import finishes straight away whatever state Calibre is in. The worker also runs once a minute on its own. What it does:

- **A Calibre that is closed or unreachable gets the books when it comes back.** Before each run the worker checks that the bridge answers (five second timeout). If it does not, the run stops and nothing counts against the books, so a Calibre that stays closed for a week loses nothing. Once Calibre is running again, the queued books arrive within about a minute. In [pull mode](#pull-mode-calibre-fetches-books-from-bindery) the worker stands down and the plugin takes the queued books at its next check instead, every 60 seconds by default.
- **Failures are retried with backoff.** A push Calibre answers with an error is retried after 1 minute, 5 minutes, 15 minutes, 1 hour, 6 hours and then daily. After 8 failed attempts the book is marked failed and left alone. A few errors are marked failed at once because waiting cannot fix them: `bad_format` (Calibre does not take that file type) and `path_forbidden` (the bridge refuses the path, or in pull Bindery refuses to serve a file outside its library folders).
- **A file deleted before it was delivered is skipped**, not retried.
- **The metadata is read when the book is delivered**, not when it was imported, so a title fixed in Bindery while Calibre was closed arrives fixed.
- Each ebook file is queued once. Audiobooks are never queued, and neither is anything imported in External import mode.
- **Every ebook format of a book reaches the same Calibre record** with Bindery Bridge 0.7.0 or later. A book with an EPUB and a PDF gets one Calibre record holding both, not two records. The preferred format goes first and makes the record (EPUB, then KEPUB, AZW3, MOBI, PDF, then anything else), and the others are added to it. With an older bridge, or in `calibredb` mode, the first format still arrives and the others are skipped with the reason `bridge cannot add a second format; update the Calibre plugin to 0.7.0`. Nothing needs doing after the update: once the bridge reports it can add formats, Bindery puts those skipped files back in the queue and delivers them to the record on its own.
- **You can see the queue.** The Calibre tab shows a **Delivery queue** line with how many books are waiting, delivered and failed, when the last one was delivered, and whether Calibre was reachable the last time the worker had something to send (with the error when it was not). Below it a **Failed deliveries** table lists each failed book with its error code, the error, the attempts and the last try. **Retry failed** puts those books back in the queue, **Clear waiting** drops everything still waiting, and **Reset delivery state** forgets every delivery (see Troubleshooting). A book's own page shows a small chip, **Waiting for Calibre**, **In Calibre** or **Calibre failed**, whenever the integration is on.

The first failure of a book is logged as `calibre delivery: add failed, will retry` with the reason, and a book that is given up on as `calibre delivery: giving up on a book`. Each run that did something logs one `calibre delivery pass` line with the counts.

**`books.calibre_id` means the id in the library you import from.** That is the library at **Library path**. The Calibre id a delivery got is kept on the delivery record instead. A delivery only fills a book's Calibre id when the book has none yet, did not come from a Calibre library import, and was delivered into that same library (or no library path is set).

### `calibredb` CLI

Bindery shells out to `calibredb add --with-library <lib> <metadata...> <file>`, where the metadata arguments carry the title, authors, cover, identifiers, language, series, series index and tags Bindery holds, and then sets the remaining fields on the new Calibre id. Requirements:

- the Calibre library path must be visible inside the Bindery container or process
- `calibredb` must be on `PATH`, or **Binary path (optional)** (`calibre.binary_path`) must point at it
- the official Docker image is distroless and does **not** ship `calibredb`. Either bind mount a Calibre install into the container and set the binary path, or run the Bindery binary on a host that has Calibre installed. `calibredb unreachable` from **Test connection** means this requirement is not met.

Only one process should write a Calibre library at a time. If the Calibre desktop app or Calibre-Web has the same library open, `calibredb add` may fail or the other program may not see the new book until it reloads.

### Calibre Bridge plugin

The [Bindery Bridge plugin](https://github.com/vavallee/bindery-plugins) runs inside a Calibre process (desktop or server) in another container or on another host, and Bindery POSTs to it. Running the Calibre desktop app on Windows? Follow [Calibre on a Windows desktop](Calibre-Windows-Desktop-Wiki.md), which leads with [pull mode](#pull-mode-calibre-fetches-books-from-bindery) because it needs no shared drive, and covers the share path and the firewall for push. Points that decide whether push works:

- **The file is not uploaded.** Bindery sends the file's path (plus the book's metadata; an older plugin that rejects the metadata payload gets a path only retry) and Calibre opens that path itself. So the Bindery library must be mounted into the Calibre container too. If it sits at a different path there, set **Push path remap** (`calibre.push_path_remap`) as `from:to` pairs, for example `/books:/mnt/user/media/books`. The remap applies to the cover path as well as the book file, so a bridge that can apply covers needs to see both.
- **A 409 from the plugin counts as success.** It means Calibre already has the book; Bindery records the returned Calibre id and moves on. This is what makes re pushing idempotent. How the bridge decides it already has the book depends on its version. From 0.6.0 it tries the `bindery` identifier Bindery stamps on every push, then `isbn`, `asin`, `google` and `hardcover`, then Calibre's own identical book check. Before 0.6.0 it tried the `bindery` identifier alone, so a book Calibre already held from anywhere else was added a second time, and the first **Push all to Calibre** into a library Calibre had already filled duplicated all of it.
- **Push all to Calibre** is only offered in plugin mode. It no longer sends anything itself: it puts every ebook file of every imported and monitored book into the delivery queue, unless the queue already holds that file in any state, and the worker delivers them, preferred format first. So a file that was delivered before is never sent again, a book already in Calibre still gets its missing formats added to its record, and Calibre does not have to be open when you click it; the books wait until it is. A book whose delivery failed is not retried by Push all either; use **Retry failed** for that. The progress modal reads the queue: the counts are per file. **Pushed** is files Calibre newly took in this run, whether as a new record or as a format added to one, **Already in Calibre** includes files delivered before it, **Failed** lists each failure with its error, and **Skipped** names why each left out book was left out, so an empty run says whether the library was already in Calibre or whether every book was filtered out. The modal can be closed while books are still waiting; the queue carries on. It stays plugin only because `calibredb` has no "already in the library" answer: a bulk run there would turn every book the library already holds into a failure.
- **Settings changes take effect on the next delivery run.** Switching mode, or correcting the plugin URL, the API key or the push path remap, no longer needs a Bindery restart. Before v1.38.0 the client was built once at startup, which is why "I fixed the remap and nothing changed" was a common follow up.
- **Push all to Calibre and an import never send the same book twice.** Both only add to the delivery queue, which holds one row per file and skips a file it already has, and only the worker sends.

#### What the bridge can do depends on its version

The bridge advertises a capability list on `GET /v1/health` and Bindery only uses what is advertised, so an older plugin keeps working unchanged. What each capability turns on:

| Capability | Since | What Bindery does with it |
|---|---|---|
| `book_metadata` | 0.5.0 | Sends the full metadata object with the push. Without it, Bindery sends the path alone |
| `cover` | 0.6.0 | Sends Bindery's cover alongside the book, so a plugin mode book is not limited to the artwork embedded in the file |
| `path_probe` | 0.6.0 | **Test connection** asks whether Calibre can actually see the Bindery library root, and names the push path remap when it cannot |
| `metadata_update` | 0.6.0 | On a 409 for a book Bindery itself pushed earlier, fills fields the Calibre row is missing rather than stopping at the duplicate |
| `error_codes` | 0.6.0 | Tells a bad path apart from bad metadata, so a mount problem stops being reported as a metadata problem and stops costing two requests per book |
| `add_format` | 0.7.0 | Sends a book's second and later formats with `addFormat`, so each joins the Calibre record the first one made. Without it those formats are skipped with a reason, and delivered automatically once the plugin is updated |

Bindery retries a 503 (the library is mid swap) with exponential backoff to about thirty seconds. If Calibre is still busy after that, the book stays queued for the next run without counting as a failed attempt. Each individual request has a thirty second timeout.

**Metadata updates are deliberately narrow.** Bindery asks the bridge to update a Calibre row only when its delivery records show it delivered that exact Calibre id for this book into this library before, meaning it created the row itself. The first time a push meets a row Bindery did not create, it records the link and writes nothing, because that row may be one you curated by hand. The bridge is also fill only on its side: it writes fields the row has left empty and never overwrites or clears one.

### Pull mode: Calibre fetches books from Bindery

Everything above describes the push transport, where Bindery connects to the plugin and hands it a file path. Pull reverses the direction: the plugin connects out to Bindery, downloads each queued book over HTTP(S) into a temporary file, adds it to Calibre, and tells Bindery how it went. It needs **Calibre Bridge 0.8.0 or later**.

**When to use it.** Calibre runs somewhere Bindery cannot easily reach or share files with: a desktop PC on another network, a laptop that comes and goes, a machine behind a router you do not control. Pull removes, in one go:

- the shared drive, because the file travels over HTTP instead of being opened by path
- the **Push path remap**, for the same reason
- the inbound firewall rule on the Calibre machine, because Calibre only makes outgoing connections
- a fixed address for the Calibre machine, because Bindery never has to find it

What it still needs is a Bindery URL the Calibre machine can reach, ideally over HTTPS if it crosses anything but your own network.

**Setting it up.**

1. On the Calibre tab, set **Write integration** to **Calibre Bridge plugin** and **Transport** to **Pull: Calibre fetches books from Bindery**. Plugin URL and Push path remap disappear; they are not used.
2. Set **API key** to a random string of at least 16 characters. Bindery refuses every pull request while the key is empty or shorter.
3. In Calibre, open the plugin's **Customize** dialog. Put the same key in **API key**, and under **Pull from Bindery** tick **Fetch books from Bindery (pull mode)** and enter Bindery's URL (including `BINDERY_URL_BASE` if you use one) in **Bindery URL**. **CA file** is only for a Bindery whose certificate comes from a private certificate authority or is self signed. Click OK; saving starts a pull at once.

Pull delivers only to the Calibre library that was open when it was turned on, and pauses while any other one is open. The plugin reports what it is doing in its **Pull status** line; the [Windows runbook](Calibre-Windows-Desktop-Wiki.md#pull-troubleshooting) lists each message and its fix.

The plugin then checks in on its own. **Test connection** does not probe anything in pull mode; it reports when Calibre last checked in and with which plugin version, and so does the **Delivery queue** line. "Has not checked in since Bindery started" means the plugin has not reached Bindery: check the URL and key in the plugin and that Calibre is open. The check in time is held in memory, so it is blank after a Bindery restart until the plugin's next request.

**What stays the same.** Books go through the same delivery queue, with the same retries and backoff, the same preferred format order, the same rule that a second format needs `add_format` (0.7.0 and later have it), and the same **Push all to Calibre**, **Retry failed** and **Reset delivery state**. While pull is on, Bindery's own delivery worker stands down, so a book is never sent both ways. A book's second format is offered to the plugin only once its first has been acknowledged, so the first makes the record and the others join it. The plugin lists once more after acknowledging a new book, so every format usually lands in the same pass.

**About the key.** In pull mode the key works in both directions: the plugin sends it to Bindery on every request, and with it anyone can list and download every book waiting in the queue. So:

- A plugin pointed at the wrong or a hostile URL hands that server your key. Only enter a Bindery URL you trust.
- Use HTTPS when Bindery is not on the same machine. The plugin warns in its status when the URL is plain `http` to a host other than its own machine. It verifies certificates against the system store plus the optional **CA file**, with no way to turn the check off, and it refuses redirects, so the key never follows one to another address.
- The key is not the Bindery API key and is not accepted anywhere else in Bindery; the Bindery API key and a browser session are not accepted on the pull routes either. It is required in every auth mode, including Disabled and Local only.
- Repeated wrong keys from one address are rate limited on their own counter, so a plugin with an old key cannot lock you out of the web UI. The answer is `429` with `Retry-After`, and the plugin waits that long before trying again. After a `401` it waits an hour; saving its settings retries at once.
- To rotate it, change it in Bindery and in the plugin.

The routes are documented in [API.md](API.md#calibre-bridge-pull).

## Topology 2: mirror into the CWA ingest folder

Set **Ingest folder path** under the Calibre-Web-Automated heading on the Calibre tab to the folder CWA watches (CWA's docs use `/cwa-book-ingest`), mounted into both containers at that path. After every successful ebook import Bindery copies the imported file there. CWA picks it up, files it into its own library, and deletes the ingest copy. What the code does, exactly:

- it is always a **copy**, never a move or a hardlink, because CWA deletes whatever it consumes and Bindery's own library must stay intact
- the copy lands in the folder root under the file's **flat basename**, whatever your naming template produced, so two books with the same file name will collide in the ingest folder
- it runs for **ebooks only**; audiobooks are never mirrored
- it runs in Auto, Move, Copy and Hardlink import modes, and does **nothing in External mode** (see the next section for that setup)
- it is **independent of the write integration**: it runs whether `calibre.mode` is Off, `calibredb` or plugin. Pointing both at the same Calibre library means the same book reaches it by two routes; read the duplicates note in troubleshooting before turning both on.

This is the topology for "Bindery keeps my library, CWA should also see new ebooks".

## Topology 3: an external tool owns the library

Choose this when CWA, Calibre auto ingest or Storyteller should be the only thing writing the library and Bindery should stay out of it. Under Settings, General tab, File Naming, set **Import Mode** to `External`; the drop folder fields appear underneath.

- **Drop folder** (`import.drop_folder`) is the folder the tool ingests from, for example `/cwa-book-ingest`. Empty means Bindery hands off in place: the download stays in the download directory and nothing is copied anywhere.
- **Layout** (`import.drop_layout`): `flat` puts every book file the download carries in the folder root under a sanely named name, which is what watch folder tools expect, so a download holding two ebook files produces two; `templated` recreates the `{Author}/{Title (Year)}/…` tree from your naming template inside the drop folder.
- **Placement** (`import.drop_link_mode`): `copy` (the default, safest since the tool usually deletes what it consumes) or `hardlink` (no extra disk, same filesystem only). The download itself is never moved, so torrents keep seeding.
- **Pair gating** (`import.drop_pair_gating`, off by default) holds the first format of a book wanted in both formats until its sibling arrives, so a tool such as Storyteller that pairs an ebook with its audiobook ingests them together. `import.drop_pair_gating_timeout_hours` (default 72) releases a held format that waited alone too long. There is no toggle for it in the UI yet; set it with `PUT /api/v1/setting/import.drop_pair_gating` and body `{"value": "true"}`, which is an admin only route, so use an admin session cookie or an admin API key.

- **Audiobook import mode** (`import.audiobook.mode`) sits under the Import Mode buttons and defaults to **Same as ebooks**. Set it when audiobooks belong somewhere else. CWA ingests `m4b` too, so in a CWA plus Audiobookshelf setup a shared External mode files audiobooks into the Calibre library; set this to `Copy` (or `Hardlink` or `Auto`) and audiobooks are placed in `BINDERY_AUDIOBOOK_DIR` instead, with the usual Audiobookshelf scan trigger, while ebooks still drop into CWA's folder.
- **Audiobook drop folder** (`import.audiobook.drop_folder`) is where audiobooks go when their mode is External, for example a folder Audiobookshelf or Storyteller watches. Empty uses the drop folder above. Pair gating only holds a format when its sibling is handed off too, and releases each format into its own folder.

After the drop Bindery parks the download as *handed off* and the book stays Wanted until the next **library scan** finds the managed copy the tool produced. That only works if `BINDERY_LIBRARY_DIR` (and `BINDERY_AUDIOBOOK_DIR`) point at where the tool finally writes, not at the drop folder and not at `metadata.db`.

The write integration and the CWA mirror do not run in this mode; the drop folder is the whole hand off.

## Reading an existing Calibre library

Separate from all of the above, and it works alongside any topology: **Library import** on the Calibre tab reads `metadata.db` and creates authors, books and editions from it, with the file paths tracked directly so those books arrive with their files attached. It does not need the write integration to be on. See [Bringing in an existing library](User-Guide-Wiki.md#bringing-in-an-existing-library) in the user guide.

## Troubleshooting

**Grabbed and imported, but the book never appears in Calibre or CWA.**

1. Check which topology you actually configured. The most common gap is none of them: `calibre.mode` Off, no ingest folder, import mode not External. Bindery then places the file in its own library and stops, and Calibre has no way to know.
2. If you pointed `BINDERY_LIBRARY_DIR` at the Calibre library folder expecting Calibre to notice, see the first section: it will not. Move Bindery's library elsewhere and pick a topology.
3. Write integration on `calibredb`: run **Test connection**. `calibredb unreachable` means the binary is not inside the container (distroless image) or the path is wrong. The log line `calibre delivery: add failed, will retry` carries calibredb's own output, but read it before assuming a fault: when calibredb skips a file it already has, it prints no `Added book ids` line and Bindery reports that as the same failure.
4. Write integration on plugin: run **Test connection**. Against a bridge that supports `path_probe` this also checks that the Calibre container can see the Bindery library root and says so by name when it cannot. Test connection now also probes one real book through the remap, the newest imported ebook or else the first file under the library root, and names the exact path Calibre was asked to open, so a remap that covers the root but breaks every book path fails here instead of on the first push. If it passes but pushes fail anyway, the Calibre container cannot open the specific path Bindery sent; mount the Bindery library into it or set **Push path remap**. The log line `calibre delivery: add failed, will retry` carries the plugin's own message and code. The plugin path has a separate line for the harmless case, `calibre delivery: book already in Calibre`, so a duplicate there is never logged as a failure.
5. CWA mirror: only ebooks are copied, and only outside External mode. Check the log for `cwa: file copied to ingest folder` or `cwa: copy to ingest folder failed`, and confirm the ingest path is the same mount in both containers.
6. External mode with a drop folder: confirm the tool consumed the file, then check the Bindery log after the next library scan. If the book stays Wanted, `BINDERY_LIBRARY_DIR` is not where the tool writes.

**Books imported while Calibre was closed.** They wait in the delivery queue and reach Calibre when it is reachable again: in push within about a minute of it running, in pull at the plugin's next check, 60 seconds by default. There is nothing to do. If one still has not arrived, look for `calibre delivery: add failed, will retry` or `calibre delivery: giving up on a book` in the log: those are books Calibre answered with an error, not books that were waiting for it.

**A book was marked failed.** Calibre answered with an error eight times over about a day and a half, or with an error that waiting cannot fix (`bad_format`, `path_forbidden`). The **Failed deliveries** table on the Calibre tab lists it with the code and Calibre's error, and the log line `calibre delivery: giving up on a book` says the same. Fix the cause, most often the push path remap, then click **Retry failed**. **Push all to Calibre** does not retry it: a book already in the queue, failed or not, is left as it is.

**Books sit in Waiting and nothing arrives.** In pull mode, look for **Calibre last checked in** on the **Delivery queue** line and read the plugin's **Pull status**; [Pull troubleshooting](Calibre-Windows-Desktop-Wiki.md#pull-troubleshooting) covers each message. In push, look at the reachability part of the **Delivery queue** line. `Calibre not reachable` with an error means the worker cannot reach the bridge: Calibre is closed, the plugin URL or API key is wrong, or a firewall is in the way. Fix that and the books go out on the next pass, within a minute. If you do not want them sent at all any more, **Clear waiting** drops them; the record of what was already delivered is kept.

**You pointed Bindery at a different Calibre library.** The queue still remembers what went to the old library, so Push all would skip every book as already delivered. Click **Reset delivery state** on the Calibre tab and confirm. It forgets every delivery without touching either library, and nothing is sent anywhere until you run **Push all to Calibre** (or import new books), which then fills the new library. Do not use it for anything else: after a reset, Bindery no longer knows which books it already put into the current library.

**The same book reaches Calibre by two routes.** Topology 1 and topology 2 are both on against the same Calibre library, or `Push all to Calibre` ran against a library the ingest folder also feeds. Whether that ends as one row or two is Calibre's call rather than Bindery's, and it turns on which route arrives first. In `calibredb` mode Bindery runs `calibredb add` without `--duplicates`, so Calibre itself skips a file whose title and author it already holds, and CWA applies its own rules to what it ingests. The plugin does not fall back on that check. From bridge 0.6.0 it runs its own ladder instead, the `bindery` identifier then `isbn`, `asin`, `google` and `hardcover` then Calibre's identical book check, which catches a library filled by another route. Before 0.6.0 it matched on the `bindery` identifier alone, so the first **Push all to Calibre** into a library Calibre already filled added every book a second time. Where the two routes disagree about the title or the author, say Bindery's canonical metadata against the file's embedded tags, nothing matches and you get two rows. Either way, pick one route and turn the other off.

**`calibredb unreachable` or `binary_path ... not found`.** The official image does not ship Calibre. Bind mount a Calibre install and set **Binary path (optional)**, or switch to the Bridge plugin, which needs no Calibre binary on Bindery's side.

**Books land in the drop folder and stay there.** The external tool is not watching that folder, or has no permission to delete from it. That is a tool side problem; Bindery has done its part once the file is in the folder.

**Audiobooks never reach CWA.** Expected. The ingest mirror is ebook only. For audiobooks use Audiobookshelf's library scan trigger or the drop folder in External mode.

**Audiobooks end up in the Calibre library.** Import Mode is External and audiobooks follow it into CWA's ingest folder, which accepts `m4b`. Set **Audiobook import mode** to `Copy`, `Hardlink` or `Auto` so audiobooks go to the audiobook folder, or give them their own **Audiobook drop folder**.

**Audiobooks never reach Calibre either.** Also expected, and this page used to claim otherwise. The write integration hands over one book file; an audiobook import produces a folder. The bridge derives the format from the file extension and rejects a folder outright, and `calibredb add` on a folder scans it for the formats in Calibre's own book extension list, which carries no audio format, so it finds nothing to add. Bindery no longer sends either one, and audiobooks are never queued for delivery: up to v1.37.x every audiobook import in plugin mode produced two doomed requests and a misleading `calibre: add failed, continuing` warning. Use Audiobookshelf or External mode for audiobooks.

**Push all to Calibre reports zero.** The progress modal now has a fourth tile, **Skipped**, and a table naming why each book was left out. Books that have not been imported yet are not listed, because with no file they were never candidates. The reasons are `not monitored` (the bulk push has always inherited this filter, it was simply invisible), `no file on disk` (an imported row whose file went missing), `audiobook only with no ebook` and `ebook file not tracked` (a book that only has the old single file path and no tracked ebook file behind it; a library scan fixes it). A book the worker skipped because its file vanished before delivery is listed there too. A run with nothing to do says which of those it hit instead of a generic message. A run where every book shows under **Already in Calibre** is not a zero: those books were delivered earlier and were rightly not sent again.

## See Also

- [`docs/DEPLOYMENT.md`](./DEPLOYMENT.md), section "Handing off to another library tool"
- [`docs/Storage-And-Hardlinks-Wiki.md`](./Storage-And-Hardlinks-Wiki.md) for the import modes
- [`docs/User-Guide-Wiki.md`](./User-Guide-Wiki.md) for the catalogue first model
- [`docs/Calibre-Windows-Desktop-Wiki.md`](./Calibre-Windows-Desktop-Wiki.md) for the plugin with Calibre desktop on Windows, pull or push, step by step
- [Bindery Bridge plugin](https://github.com/vavallee/bindery-plugins)
