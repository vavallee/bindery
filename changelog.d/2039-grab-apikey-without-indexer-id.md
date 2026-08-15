### Fixed

- **Grabs from API clients no longer fail with the indexer's 401** (#2039) —
  search and queue responses strip the indexer apikey out of `nzbUrl` and the
  grab handler puts it back from the `indexerId` the web UI sends along. A
  client that posts only `{guid, nzbUrl}` (scripts, `curl`, other API consumers)
  has no id to send, so the unsigned URL went to the download client and the
  indexer answered `401` — surfaced as `failed to send to downloader: fetch nzb:
  indexer returned HTTP 401`. The key is now also recoverable from the
  configured indexer whose host matches the download URL.
