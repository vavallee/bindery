### Fixed
- **All enabled download clients are now polled each import cycle** — previously `CheckDownloads` fetched only the first enabled client, so users who configured e.g. SABnzbd alongside qBittorrent had downloads from the secondary client permanently stuck at "downloading" (#1090).
