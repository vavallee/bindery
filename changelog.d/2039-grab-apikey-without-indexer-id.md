### Fixed

- **Grabs from API clients no longer fail with the indexer's 401** (#2039) —
  search and queue responses strip the indexer apikey out of `nzbUrl` and the
  grab handler puts it back from the `indexerId` the web UI sends along. A
  client that posts only `{guid, nzbUrl}` (scripts, `curl`, other API consumers)
  has no id to send, so the unsigned URL went to the download client and the
  indexer answered `401` — surfaced as `failed to send to downloader: fetch nzb:
  indexer returned HTTP 401`. The key is now also recoverable from the
  configured indexer whose host matches the download URL.

### Security

- **The grab response no longer echoes the indexer apikey back to the caller**
  (#2039) — the queue listing already stripped the shared indexer credential out
  of `nzbUrl`, but the grab response handed back the download URL that had just
  been signed, so a non-admin who grabbed a release read the key straight out of
  the reply. Indexer credentials are admin-only settings. The stored row keeps
  the key, so retries still reach the indexer authenticated.
