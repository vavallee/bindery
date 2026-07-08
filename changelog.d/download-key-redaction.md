### Security
- **Indexer API keys no longer leak into errors** — when a torrent/NZB fetch failed at the transport layer (timeout, DNS, TLS), the underlying `url.Error` carried the full signed download URL, including the indexer's `apikey`, into the download row, history, and webhook/Discord notifications. All five download-client fetch paths now scrub the key before the error is wrapped.
