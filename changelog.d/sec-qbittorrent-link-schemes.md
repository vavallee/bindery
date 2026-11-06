### Fixed

- **qBittorrent only receives http, https and magnet links** (#TBD). The link scheme is now read case insensitively, so `HTTPS://` links are fetched by Bindery like any other web link, `MAGNET:` links still work, and links with any other scheme (or none) are refused with a clear error instead of being passed on to qBittorrent.
