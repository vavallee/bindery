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

Set the import mode in **Settings → File Naming**.

| Mode | Extra disk | Seeding | Notes |
|---|---|---|---|
| **hardlink** | none | kept | Recommended for torrents. The completed file is linked into the library instantly; the download client keeps seeding the same data on disk. Requires downloads and library on one filesystem. |
| **copy** | doubled | kept | Use when downloads and library are on different filesystems. Copies into the library and leaves the download in place so it can keep seeding. |
| **move** | none | **broken** | Moves the file out of the download location, so a torrent can no longer seed it. Only suitable for Usenet, or when you do not seed. |

## Multi-disc audiobook flattening

Some audiobook releases arrive as nested disc folders with repeated track names, for example `Disc 1/Track 01.mp3`, `Disc 1/Track 02.mp3`, `Disc 2/Track 01.mp3`. Audiobook players that sort each disc folder independently (or treat repeated `Track 01` names as duplicates) play these in the wrong order.

Enable **Settings → General → File Naming → Flatten multi-disc audiobooks** to import such a download into a single flat folder, renaming tracks to `Part 001.ext`, `Part 002.ext`, … in disc-then-track order. Disc numbers are detected from folder names like `Disc 1`, `Disk 02`, `CD 3`, `Part 4`; track numbers from file names like `Track 01.mp3`, `Chapter 02.mp3`, or a leading `01 - Title.mp3`. Root-level sidecars (cover art, cue sheets) are carried across.

Guarantees:

- **Off by default.** Single-disc audiobooks and downloads with no disc folders are never altered.
- **Copy/hardlink only.** Flattening is enforced backend-side to run only when the resolved import mode is `copy` or `hardlink`, because it renames files and must never touch a still-seeding torrent source. In `move` and `external` mode the setting is ignored and the existing whole-folder behaviour applies.
- **Seeding preserved.** The source download is copied or hardlinked, never moved or renamed, so it keeps seeding.

## Download folders

| Variable | Purpose |
|---|---|
| `BINDERY_DOWNLOAD_DIR` | Where completed downloads land. Default `/downloads`. |
| `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` | Optional separate folder for audiobook downloads. Falls back to `BINDERY_DOWNLOAD_DIR`. |
| `BINDERY_LIBRARY_DIR` | Ebook library destination. |
| `BINDERY_AUDIOBOOK_DIR` | Audiobook library destination. |

## Per-author audiobook root folder

By default, every author's audiobooks are imported to the global audiobook destination (`BINDERY_AUDIOBOOK_DIR`, which itself falls back to the ebook library when unset). You can override that destination for a single author.

Open the author, click **Edit**, and use the **Audiobook root folder** selector in the Edit Author modal:

- Pick any configured root folder to send **that author's** audiobooks there instead of the global audiobook destination.
- Leave it on **Use global audiobook folder** (the default) to fall back to `BINDERY_AUDIOBOOK_DIR`.

This is a separate setting from the author's ebook **Root folder** — choosing a custom ebook root never changes where the author's audiobooks land, and vice versa. That keeps audiobooks out of the ebook tree even when an author has a custom ebook root.

The override applies wherever Bindery decides an audiobook's location: regular imports of completed downloads, Library Scan matching, and the Audiobookshelf importer's file-visibility checks. When the per-author audiobook root is unset, all of those fall back to the global audiobook directory.

## Torrent vs Usenet folders

There is **no per-protocol download folder setting**, and you do not need one. Each download client (qBittorrent, SABnzbd, NZBGet) decides where it places completed files in its own configuration, so they are already separate.

Point them at subfolders of a common root — for example `/data/downloads/torrents` and `/data/downloads/usenet` — set `BINDERY_DOWNLOAD_DIR` to that root, and Bindery reads each completed download from the path the client reports. Bindery accepts completed downloads anywhere at or under `BINDERY_DOWNLOAD_DIR`, so there is no need to consolidate them into one folder.
