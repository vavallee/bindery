### Added
- **Calibre push path remap** (#1346) — Bindery hands the Bindery Bridge
  plugin the exact path it stores each book at, and the plugin opens that
  path on *its* side of the container boundary; when the two containers mount
  the library at different points (the recurring Unraid case), every push
  failed with "No such file or directory". A new **Settings → Calibre → Push
  path remap** field (plugin mode) translates Bindery's library prefix to the
  Calibre container's before the push, using the same `from:to[,from:to]`
  grammar as `BINDERY_DOWNLOAD_PATH_REMAP` — e.g.
  `/books:/mnt/user/media/books`. Malformed pairs are rejected at save time;
  empty means no translation, and aligning the mounts remains the preferred
  zero-config setup.
