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

## Download folders

| Variable | Purpose |
|---|---|
| `BINDERY_DOWNLOAD_DIR` | Where completed downloads land. Default `/downloads`. |
| `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` | Optional separate folder for audiobook downloads. Falls back to `BINDERY_DOWNLOAD_DIR`. |
| `BINDERY_LIBRARY_DIR` | Ebook library destination. |
| `BINDERY_AUDIOBOOK_DIR` | Audiobook library destination. |

## Torrent vs Usenet folders

There is **no per-protocol download folder setting**, and you do not need one. Each download client (qBittorrent, SABnzbd, NZBGet) decides where it places completed files in its own configuration, so they are already separate.

Point them at subfolders of a common root — for example `/data/downloads/torrents` and `/data/downloads/usenet` — set `BINDERY_DOWNLOAD_DIR` to that root, and Bindery reads each completed download from the path the client reports. Bindery accepts completed downloads anywhere at or under `BINDERY_DOWNLOAD_DIR`, so there is no need to consolidate them into one folder.
