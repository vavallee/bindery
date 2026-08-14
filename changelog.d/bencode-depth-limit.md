### Security

- **A malicious torrent file could crash Bindery** — the bencode walk that
  extracts a torrent's infohash recursed once per nesting level with no bound,
  so a crafted `.torrent` of deeply nested lists exhausted the goroutine stack
  and took the whole process down. A stack overflow is not a recoverable panic,
  so the container restarted and the grab retried into another crash. Nesting is
  now capped at 64 levels (a real torrent nests 3-4) and an over-nested file is
  refused like any other malformed payload. Reachable from any indexer serving
  the response to a grab.
