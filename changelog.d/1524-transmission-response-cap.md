### Fixed
- **Transmission polling on large torrent histories** (#1524) — the download
  poller read at most 1 MiB of Transmission's `torrent-get` reply, so an
  instance with a few thousand torrents (one report: ~12 MiB for 5000+) had its
  response silently truncated and every poll failed with "unexpected end of
  JSON input", blocking imports. The RPC read cap is now 64 MiB, and a reply
  that somehow still exceeds it returns a clear "too many torrents to poll in
  one request" error instead of invalid JSON.
