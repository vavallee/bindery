### Changed
- **One shared torrent fetch for all four torrent clients** (#1470). Torrent files fetched from an indexer now go through a single code path in qBittorrent, Deluge, Transmission and rTorrent. Transmission now rejects empty or oversized torrent files instead of passing them on, and torrent fetches time out after 60 seconds in Deluge and qBittorrent (it was 15).
