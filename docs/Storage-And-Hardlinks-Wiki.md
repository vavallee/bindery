# Storage layout and hardlinking

## Recommended layout: a single /data mount

Mount one volume so downloads and the library sit on the **same filesystem**:

```
/data
  /downloads    completed downloads (your download clients write here)
  /media        your library (Bindery writes here)
```

This is the standard *arr layout. It matters because **hardlinks only work within a single filesystem** — if downloads and library are separate mounts, Bindery cannot hardlink between them.

## Import modes

Set the import mode in **Settings → General → File Naming**.

| Mode | Extra disk | Seeding | Notes |
|---|---|---|---|
| **auto** *(default)* | none / doubled | kept | Recommended. Hardlinks when the download folder and library share a filesystem (no extra disk), otherwise copies (doubled). The source stays in place either way, so torrents keep seeding. Picks hardlink or copy automatically per download. |
| **hardlink** | none | kept | Forces hardlinking for torrents. The completed file is linked into the library instantly; the download client keeps seeding the same data on disk. Requires downloads and library on one filesystem. |
| **copy** | doubled | kept | Forces copying. Use when downloads and library are on different filesystems. Copies into the library and leaves the download in place so it can keep seeding. |
| **move** | none | **broken** | Moves the file out of the download location, so a torrent can no longer seed it. Only suitable for Usenet, or when you do not seed. |
| **external** | none | kept | Hands off to a sibling tool (Calibre, CWA, Grimmory, Storyteller). Bindery stops after grabbing; the external tool processes and places the file, then Bindery reconciles it on the next library scan. Can drop the file into a configured watch folder, filled by copy (the default) or hardlink, set alongside the drop folder fields. |

### A different mode for audiobooks

**Audiobook import mode** (`import.audiobook.mode`), just under the mode buttons, lets audiobooks use a mode of their own. It defaults to **Same as ebooks**, which is how every install behaved before it existed: one mode for every download. Pick any of the modes above to override it for audiobooks only; the **Import Mode** buttons then apply to ebooks.

The setup this exists for is Calibre-Web-Automated plus Audiobookshelf. CWA accepts `m4b` too, so with a single External mode it would file audiobooks into the Calibre library where Audiobookshelf never sees them. Instead:

- **Import Mode** `External`, **Drop folder** `/cwa-book-ingest`: ebooks are handed to CWA.
- **Audiobook import mode** `Copy` (or `Hardlink`, or `Auto`): audiobooks are placed in `BINDERY_AUDIOBOOK_DIR` (or the author's audiobook root folder), and Bindery triggers the Audiobookshelf library scan as usual.

`Copy` works on any storage, including an Unraid mergerfs pool where hardlinks fail. The reverse split works too, as does External for both with a separate **Audiobook drop folder** (`import.audiobook.drop_folder`); when that is empty audiobooks use the ebook drop folder. Layout and Placement for the drop folder apply to both formats. The mode is decided per download from its files, or from the format you pick in manual import.

To check a real setup, press **Diagnose** on a download client in **Settings → Download clients**. It tries a real hardlink from each folder that client's grabs land in (ebook and audiobook, when they differ) to every library folder and reports one row per pair, with the reason when a link fails (different filesystems, separate Docker bind mounts that share a device ID, or a filesystem that refuses links). Every pair is probed on its own, so two bind mounts of the same filesystem are caught even though they report the same device ID. Bindery only runs the probe when the client's folder, after the path remap, is under a configured download or library folder.

If you pick **hardlink** on a setup that cannot hardlink — separate Docker volume mounts for downloads and library are the usual cause, and they look like sibling paths — the selector warns right under the buttons and says why. Imports still work; they just copy. Put both under one mount, or use **auto**, which makes that decision per download.

## Multi-disc audiobook flattening

Some audiobook releases arrive as nested disc folders with repeated track names, for example `Disc 1/Track 01.mp3`, `Disc 1/Track 02.mp3`, `Disc 2/Track 01.mp3`. Audiobook players that sort each disc folder independently (or treat repeated `Track 01` names as duplicates) play these in the wrong order.

Enable **Settings → General → File Naming → Flatten multi-disc audiobooks** to import such a download into a single flat folder. The toggle only appears when the mode audiobooks use (the Audiobook import mode, or Import Mode when that is Same as ebooks) is Copy or Hardlink; on Auto, switch the mode to see it, then switch back, because the stored setting is kept either way. Tracks are renamed to `Part 001.ext`, `Part 002.ext`, … in disc-then-track order. Disc numbers are detected from folder names like `Disc 1`, `Disk 02`, `CD 3`, `Part 4`; track numbers from file names like `Track 01.mp3`, `Chapter 02.mp3`, or a leading `01 - Title.mp3`. Root-level sidecars (cover art, cue sheets) are carried across.

Guarantees:

- **Off by default.** Single-disc audiobooks and downloads with no disc folders are never altered.
- **Never in external mode.** Flattening renames files, so it only ever places via copy or hardlink. In `copy` and `hardlink` mode the source is left untouched. In `move` mode Bindery flattens by copying and then removes the source folder, the same copy-then-delete contract move mode already uses, which is why move mode cannot seed. In `external` mode the setting is ignored and the existing whole-folder behaviour applies.
- **Seeding preserved in copy and hardlink mode.** The source download is copied or hardlinked, never renamed in place, so it keeps seeding.

## Audiobooks whose files share no folder

A torrent sometimes reports book files that sit directly at a shared download root, or spread across sibling folders with no single folder below the root containing them all. Moving that root would drag in unrelated downloads, so Bindery places those files one at a time into the book's destination folder instead.

Per-file placement flattens: every file keeps its own name and lands directly in the destination. If two of the reported files have the same filename but different contents, they would both claim the same destination path and one would replace the other.

Bindery checks for that before it creates anything. When two files would collide, the import is blocked with a message naming both source paths and the destination they share, and nothing is written: no destination folder, no partially placed tracks. Both files stay in the download folder untouched.

To import such a download, either rename one of the files at the source and retry, or use **Queue → Manual import** to place the files by hand.

## Download folders

| Variable | Purpose |
|---|---|
| `BINDERY_DOWNLOAD_DIR` | Where completed downloads land. Default `/downloads`. |
| `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` | Optional separate folder for audiobook downloads. Falls back to `BINDERY_DOWNLOAD_DIR`. |
| `BINDERY_DOWNLOAD_PATH_REMAP` | Comma separated `from:to` pairs mapping the paths your download client reports onto the paths Bindery sees. Needed when the two containers mount the same storage at different paths. |
| `BINDERY_LIBRARY_DIR` | Ebook library destination. |
| `BINDERY_AUDIOBOOK_DIR` | Audiobook library destination. |

A download folder can sit inside the library folder (for example
`/data/audiobooks/.torrents`, so imports hardlink on the same filesystem and
torrents keep seeding). The library scan leaves it out: it skips every folder
whose name starts with a dot, the configured download folders, and any folder
holding a `.binderyignore` file, so torrent copies are never listed as
unmatched books or imported a second time.

## Default audiobook root folder

Settings > Root Folders has two default pickers: **Default root folder** for ebooks and **Default audiobook root folder** for audiobooks. Either one takes priority over its env var, so you can leave `BINDERY_AUDIOBOOK_DIR` unset and choose the folder in the UI instead, which is handy on the Windows binary. Removing a root folder that is a default also clears that default, and Bindery falls back to the env var.

The two are independent: the ebook default never moves audiobooks, and the audiobook default never moves ebooks.

## Per-author audiobook root folder

By default, every author's audiobooks are imported to the global audiobook destination: the **Default audiobook root folder** when one is set, otherwise `BINDERY_AUDIOBOOK_DIR`, which itself falls back to the ebook library when unset. You can override that destination for a single author.

Open the author, click **Edit**, and use the **Audiobook root folder** selector in the Edit Author modal:

- Pick any configured root folder to send **that author's** audiobooks there instead of the global audiobook destination.
- Leave it on **Use global audiobook folder** to fall back to the Default audiobook root folder, then `BINDERY_AUDIOBOOK_DIR`.

The Add Author dialog shows the same picker when the media type includes audiobooks, preselected with the Default audiobook root folder.

This is a separate setting from the author's ebook **Root folder** — choosing a custom ebook root never changes where the author's audiobooks land, and vice versa. That keeps audiobooks out of the ebook tree even when an author has a custom ebook root.

The override applies wherever Bindery decides an audiobook's location: regular imports of completed downloads, Library Scan matching, and the Audiobookshelf importer's file-visibility checks. When the per-author audiobook root is unset, all of those fall back to the Default audiobook root folder, then the global audiobook directory.

## Torrent vs Usenet folders

There is **no per-protocol download folder setting**, and you do not need one. Each download client (qBittorrent, SABnzbd, NZBGet) decides where it places completed files in its own configuration, so they are already separate.

Point them at subfolders of a common root — for example `/data/downloads/torrents` and `/data/downloads/usenet` — set `BINDERY_DOWNLOAD_DIR` to that root, and Bindery reads each completed download from the path the client reports. Bindery accepts completed downloads anywhere at or under `BINDERY_DOWNLOAD_DIR`, so there is no need to consolidate them into one folder.
