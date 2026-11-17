# BookOrbit integration

Bindery follows authors and grabs books. [BookOrbit](https://github.com/bookorbit/bookorbit) is a self hosted library that serves them for reading. This page covers three ways to connect the two.

| Recipe | Who writes the library | Use it when |
|---|---|---|
| [1. Bindery files, BookOrbit reads](#recipe-1-bindery-files-the-library-bookorbit-reads-it) | Bindery | You want the simple setup and Bindery's own naming |
| [2. External mode with the Book Dock](#recipe-2-external-mode-with-the-book-dock) | BookOrbit | You want BookOrbit to own and organise the files |
| [3. Import an existing BookOrbit library](#recipe-3-import-an-existing-bookorbit-library) | Nobody, files stay put | You already have a BookOrbit library and want Bindery to know about it |

Bindery is MIT and BookOrbit is AGPL-3.0. This page describes what we saw BookOrbit do and links to its public project. Nothing here is copied from it.

## What was tested

| | |
|---|---|
| Recipe 2 lab | Bindery 1.51.1 (official image), BookOrbit 3.8.0 (official image, PostgreSQL 18 with pgvector). One local ext4 directory bind mounted into both containers. Downloads came from a finished SABnzbd job (Usenet). EPUB and multi track MP3 audiobooks |
| Recipe 1 lab | A Bindery binary built from main after #3947 and #3948 (API only, no web UI), against the same BookOrbit 3.8.0. Import mode Copy. Ebooks and audiobooks in separate folders on one local ext4 directory |

Not tested, anywhere on this page: Longhorn or any other cluster storage, NFS or SMB, torrents, Bindery and BookOrbit running as different users, BookOrbit's `embedded_only` metadata mode, pair gating with BookOrbit, and Kubernetes at all. BookOrbit's own README says network and distributed filesystems are not supported for paths it writes to, so treat anything other than a local disk as unproven.

Where a statement comes from reading Bindery's code rather than from the lab, the text says so.

## Recipe 1: Bindery files the library, BookOrbit reads it

Bindery imports each download into its own library the normal way. BookOrbit has libraries pointed at those folders and just reads them. Nothing is dropped anywhere and BookOrbit renames nothing.

### What works

- Bindery's default naming gave `Author/Title (Year)/Title - Author.epub` for ebooks and `Author/Title (Year)/` holding the tracks for audiobooks.
- BookOrbit listed the right title and author for every book.
- A three track MP3 audiobook is one book in BookOrbit, and a single M4B is one book.
- Series showed up for the EPUB only. BookOrbit took it from the series metadata inside the EPUB. Bindery's default templates put no series in the path and the audiobooks carried no series tag, so those listed with no series. Putting the series in the path through a naming template was not tested.
- The title comes from the file's own metadata, not the folder, so the year in Bindery's folder name (Bindery's release date, which can look odd) does not matter.

### What to set

In Bindery, set **Import Mode** to `Copy` (Settings, General tab, File Naming). Only Copy was tested here. Hardlink and Move are untested for this recipe. Hardlinks make two names for one file, so anything that edits a file in place, including a BookOrbit option that writes metadata into files if you turn it on, edits both names. Use separate roots:

| Setting | Value |
|---|---|
| `BINDERY_LIBRARY_DIR` | the ebook folder, for example `/data/ebooks` |
| `BINDERY_AUDIOBOOK_DIR` | the audiobook folder, for example `/data/audiobooks` |

In BookOrbit, create two libraries, one over each folder. Do not merge them into one (see limits).

Then decide how BookOrbit learns about new books. Both of these worked:

| Option | What to do | What we saw |
|---|---|---|
| Watch on | Turn on the library's watch setting before Bindery imports | A fresh import appeared on its own about 25 seconds later. Only the new folder was scanned |
| Manual scan | Start a scan of the library in BookOrbit | Under a second for a handful of books, and every book was listed |

With watch off, a library whose content arrived after it was created stayed at 0 books for six minutes. Creating a library starts a scan, but nothing after that does unless watch is on or you start one. Books already in the folder before you turned watch on stayed unlisted until a manual scan, because the watcher only scans what changes.

The lab turned watch on through BookOrbit's API. Watch on the ebook library was not tried, and neither was clicking through BookOrbit's UI for it.

Leave BookOrbit's file renaming off for these libraries, or Bindery loses track of files it placed. This is advice from how Bindery tracks paths. It was not tested.

### Known limits

- **One library over a parent holding both folders was refused** while the two child libraries existed (HTTP 409, folder overlap). On a copy of the tree with no child libraries it worked, but the same title in both formats showed up as two separate books, one ebook and one audiobook. Two libraries avoid this.
- **Renaming an audiobook folder leaves a ghost.** BookOrbit kept the old book as a missing entry with no files and created a second book for the renamed folder. A rescan did not clear the ghost. Keep folder names stable once a library is watched.
- **Untagged files were not tested.** Every file in the lab carried tags, so BookOrbit's folder based author parsing was never exercised.

## Recipe 2: External mode with the Book Dock

Here BookOrbit owns the library. Bindery stops after the grab, drops each finished download into the folder BookOrbit's Book Dock watches, and BookOrbit files the book into its library. Bindery's next library scan finds the file where BookOrbit put it and marks the book imported. This is the External mode topology described in [Calibre integration, topology 3](./Calibre-Integration-Wiki.md#topology-3-an-external-tool-owns-the-library), with BookOrbit as the tool.

The lab for this recipe ran on Bindery 1.51.1, before the External mode changes in #3947 and #3948. Those two are covered from the code and the current docs, and were not re-run against BookOrbit.

### How it works

1. Bindery grabs a release and the download client finishes it.
2. Bindery copies or hardlinks the book into the drop folder under a clean name. The download is never moved, so a torrent would keep seeding. The download shows as handed off and the book stays Wanted.
3. BookOrbit's Book Dock sees the file, looks the book up and, if it is confident enough, moves it into its library under its own naming. In the lab this took about 17 to 18 seconds, with no rescan and no restart.
4. Bindery's next library scan finds the file and the book becomes Imported. The scan runs every 6 hours by default (`library.scan_interval`, 15 minutes at the lowest, applied on restart) or on demand from Settings, General tab, Library, **Scan Library**.

### One tree, mounted in both

Both apps need to see the same folders, on one filesystem, so a hardlink from the download works and BookOrbit's move out of the dock is a rename.

| Folder | Bindery | BookOrbit |
|---|---|---|
| `/data/downloads` | `BINDERY_DOWNLOAD_DIR` | not used |
| `/data/book-dock` | **Drop folder** | Book Dock (`BOOK_DOCK_PATH`) |
| `/data/books` | `BINDERY_LIBRARY_DIR` and `BINDERY_AUDIOBOOK_DIR` | the library folder |

Mount the parent once, as `/data`, in both containers. Three separate mounts look the same from inside but are three filesystems to the kernel, and hardlinks between them fail.

`BINDERY_LIBRARY_DIR` must be BookOrbit's final library, not the dock. Bindery never writes there in this mode. It only reads it during a scan. If it points at the wrong place the book stays Wanted.

Bindery creates the folders it makes with mode `0750` by default, so a BookOrbit running as a different user may not be able to move files out of them. Running both as the same user avoids the question. Setting `BINDERY_DIR_MODE` to `0755` (the accepted values are `0700`, `0750` and `0755`) should open them up. Both the different user case and that fix are untested here.

`BINDERY_PUID` and `BINDERY_PGID` are sanity checks only, not user switchers. Set the user on the container itself, `user: "UID:GID"` in Compose, as in the example below. See [Running as a specific UID/GID](./DEPLOYMENT.md#running-as-a-specific-uidgid).

```yaml
services:
  bindery:
    image: ghcr.io/vavallee/bindery:1.51.1
    user: "1000:1000"
    environment:
      BINDERY_LIBRARY_DIR: /data/books
      BINDERY_AUDIOBOOK_DIR: /data/books
      BINDERY_DOWNLOAD_DIR: /data/downloads
    volumes:
      - ./bindery-config:/config
      - ./shared:/data

  bookorbit:
    image: ghcr.io/bookorbit/bookorbit:3.8.0
    environment:
      PUID: "1000"
      PGID: "1000"
      BOOK_DOCK_PATH: /data/book-dock
      # plus the database and secret settings from BookOrbit's own example env file
    volumes:
      - ./shared:/data
```

### BookOrbit settings

1. Create a library whose folder is `/data/books`. Leave its naming at the default so the author stays the top level folder (see limits).
2. In BookOrbit, turn on auto finalize for the Book Dock and point it at that library. Leave the threshold at 85 and the metadata mode at safe merge. Auto finalize is off on a fresh install, and without it every book waits in the dock for a click.

The lab set the same thing through BookOrbit's API, with an API key created in BookOrbit:

```bash
curl -X PUT https://bookorbit.example.com/api/v1/book-dock/settings \
  -H "X-Api-Key: $BOOKORBIT_API_KEY" -H "Content-Type: application/json" \
  -d '{"autoFetchMetadata":true,"autoFinalizeEnabled":true,"autoFinalizeThreshold":85,
       "autoFinalizeLibraryId":1,"autoFinalizeFolderId":1,"autoFinalizeMetadataMode":"safe_merge"}'
```

### Bindery settings

Under Settings, General tab, File Naming:

| Setting | Value | Default |
|---|---|---|
| **Import Mode** (`import.mode`) | `External` | `Auto` |
| **Drop folder** (`import.drop_folder`) | `/data/book-dock` | empty, which hands off in place and drops nothing |
| **Layout** (`import.drop_layout`) | `Flat` | `Flat` |
| **Placement** (`import.drop_link_mode`) | `Copy`, or `Hardlink` to save disk | `Copy` |
| **Audiobook import mode** (`import.audiobook.mode`) | leave on **Same as ebooks** | **Same as ebooks** |
| **Audiobook drop folder** (`import.audiobook.drop_folder`) | leave empty | empty, which uses the drop folder above |

The drop folder fields appear once Import Mode is External. With the audiobook settings left alone, both formats go through the one dock.

**Flat versus templated.** Use Flat. An ebook lands as `Pride and Prejudice - Jane Austen.epub` in the dock root. An audiobook lands as one folder, `Persuasion - Jane Austen`, holding its tracks, and BookOrbit took that in as one book with three files. Templated (an `Author/Title` tree inside the dock) also worked, including two books by one author arriving together, but it gains nothing because BookOrbit renames everything anyway. Nested `Author/Title` folders in the dock were read as separate books, which is one more reason to prefer flat.

**Copy versus hardlink.** Hardlink worked: the file in BookOrbit's library kept the same inode as the download. Because both names are one file, anything that edits it in place, including a BookOrbit option that writes metadata into files if you turn it on, edits the seeding download too. Use Copy in that case.

### What BookOrbit does with a drop

| Case | Result |
|---|---|
| One EPUB, flat, copy | Filed as `Jane Austen/Pride and Prejudice (2023)/Pride and Prejudice (2023).epub`. Bindery matched it on the next scan |
| One EPUB, flat, hardlink | Filed, same inode as the download |
| Three track audiobook, flat or templated | One book in BookOrbit with three files. Bindery matched the folder |
| Ebook and audiobook of one title | BookOrbit added the audiobook to the existing book's folder. Bindery recorded both formats on one book |
| EPUB with no title or author inside | Parked in the dock for review. Bindery kept the book Wanted |
| The same book delivered a second time | Parked in the dock, because BookOrbit will not overwrite the copy it has |
| Nested `Author/Title` folders in the dock | Became separate books |

The year in BookOrbit's folder name comes from the edition its lookup picked, so it is often a reprint year and will not match Bindery's. The scan matches on author folder and title, so this does not matter.

### When a book parks in the dock

BookOrbit scores how well a dropped file matches its metadata lookup, and the lab ran with auto finalize on at the default threshold of 85. A drop whose title and author metadata matched scored exactly 85, which equals the threshold, so it was finalized with no spare margin. A file with no usable metadata scored 0 and waited in the dock for a click, even though Bindery had given it a sensible file name.

Do not raise the threshold above 85. A matching drop scored 85 in the lab, so a higher setting would leave those files waiting in the dock too.

A parked book is silent on the Bindery side: the book stays Wanted, the download stays handed off and Bindery does not grab it again. When a book you expected has not shown up, look at the Book Dock in BookOrbit and finalize it there. Bindery picks it up on the next scan.

### Checks before the drop

Since #3948, External mode with a drop folder runs the same language check and content check a managed import runs, before anything is placed. This is from the code and current docs, not from the lab.

- With the content check on `warn` (the default for `import.ebook.content_check`), a file that looks like a different book is still dropped, and the finding is logged and shown on the drop's History row.
- With `block`, or when the file's language is not allowed by the author's metadata profile, nothing is dropped. The download is parked as blocked, the release is blocklisted and the book stays Wanted.
- With no drop folder (hand off in place) nothing is placed, so the checks do not run.

The lab saw the older behaviour on 1.51.1: a wrong book was dropped under the wanted book's name with no warning, because the checks did not run in External mode.

### Reorganize

Since #3947, Reorganize shows a warning in External mode that names the affected formats (ebook files, audiobook files, or both, as set by Import Mode and the audiobook import mode), and the Rename button stays off until you tick a confirmation. Without the confirmation the apply route answers 409 and moves nothing. Do not confirm it for a BookOrbit library. On 1.51.1 the Reorganize preview offered to move all eight BookOrbit filed books to Bindery's own names. Library scans and metadata refreshes changed nothing on disk.

### Known limits

- **Upgrades do not replace the BookOrbit copy.** A second delivery of a book BookOrbit already has is parked with a message that a file with that name already exists at the target. Nothing is overwritten or duplicated.
- **The library scan needs the author as the top level folder.** It reads `Author/Title/...` and takes the author from the first folder, even when the file's own metadata names the author. BookOrbit's default naming fits. A pattern like `Genre/Author/Series/NN - Title` does not: every file under that shape came back unmatched, with the genre read as the author.
- **No `.opf` reaches the dock.** `import.write_opf_sidecar` applies to books Bindery files itself, so it does nothing on this path, flat or templated.
- **Empty folders stay behind in the dock** after an audiobook or a templated drop is filed. They are harmless. A later drop of the same audiobook goes to `Title - Author (2)`.
- **The handed off download stays in Bindery's queue** after the book is imported, and no Book imported history event is written for a book the scan reconciles. That is how External mode works with every tool.
- **A new book can take up to one scan interval to leave Wanted.** Lower `library.scan_interval` or run a scan by hand.
- **Pair gating** (`import.drop_pair_gating`) was not tried with BookOrbit.

## Recipe 3: Import an existing BookOrbit library

If BookOrbit already holds your books, Bindery can read them with a read only API key and register each file as owned, where it sits. Nothing is moved, renamed or written to BookOrbit, and running it again is safe. This is separate from recipes 1 and 2 and can be combined with either. Setup, path remap and limits are in [BookOrbit Import](./BookOrbit-Import.md).

## Converting audiobooks in place

People often convert a multi track MP3 audiobook to one M4B after it is imported. We tried four variants with recipe 1 (Bindery filed, BookOrbit watching the audiobook library).

Bindery tracks an audiobook as one row holding the book's folder, not one row per track. That decides everything below.

| Change | Bindery after a library scan | BookOrbit with watch on |
|---|---|---|
| MP3 tracks replaced by one M4B in the same folder, any file name | Nothing changed. Still Imported, same single row, no search, nothing unmatched. Later scans stay quiet | Followed within seconds. Same book, now an M4B |
| Folder renamed (MP3 book) | The scan reconciled it: the book was pointed at the new folder and a new row was added. The old row, now dead, was kept. The book stayed Imported, no search, nothing deleted | Did not follow cleanly. The old book stayed as a missing entry and a duplicate was created. A rescan did not clear it |
| Folder renamed (book already an M4B) | Same as above: new row added, old dead row kept | Treated as a move, same book, no duplicate |

So the safe way to convert is to keep the folder name. Replace the files inside it and let BookOrbit watch. Bindery needs no action.

Not tested: a converter that keeps the MP3s next to the M4B, a conversion that moves the file into a different author folder, a folder Bindery cannot match to any book, and the scheduled six hourly scan (the on demand scan runs the same code).

## On Kubernetes

Not tested on a cluster yet. This section describes what the chart can now do and how a setup should work. It will be marked tested once it has been run.

The Helm chart has three opt in values, `extraVolumes`, `extraVolumeMounts` and `affinity`, all empty by default (#3955). Use them to mount a claim that BookOrbit also mounts, so downloads, the dock and the library sit on one volume. The details are in [Sharing a volume with another app](./DEPLOYMENT.md#sharing-a-volume-with-another-app).

```yaml
extraVolumes:
  - name: shared
    persistentVolumeClaim:
      claimName: bookorbit-shared
extraVolumeMounts:
  - name: shared
    mountPath: /data
affinity:
  podAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      - labelSelector:
          matchLabels:
            app.kubernetes.io/name: bookorbit
        topologyKey: kubernetes.io/hostname
```

Then point the paths at the mount with `env`, for example `BINDERY_LIBRARY_DIR: /data/books` and `BINDERY_DOWNLOAD_DIR: /data/downloads`, and use the same layout as recipe 2 or recipe 1.

A ReadWriteOnce claim only attaches on one node. When two pods mount the same one, they have to run on the same node, which is what the `affinity` above is for. Whether a given storage class (Longhorn, NFS and the rest) gives you working hardlinks and atomic renames, and whether BookOrbit's watcher sees changes on it, is untested.

## See also

- [BookOrbit Import](./BookOrbit-Import.md), recipe 3 in full
- [Calibre integration, topology 3](./Calibre-Integration-Wiki.md#topology-3-an-external-tool-owns-the-library), External mode in general
- [File naming, drop folder](./File-Naming-Wiki.md#drop-folder-external-import-mode), the names Bindery gives dropped files
- [Storage and hardlinks](./Storage-And-Hardlinks-Wiki.md), one mount layout and import modes
- [Deployment, handing off to another library tool](./DEPLOYMENT.md#handing-off-to-another-library-tool-cwa-calibre-storyteller)
