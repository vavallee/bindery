### Security
- **NZB grabs through Prowlarr no longer hand its API key to the indexer** (#2958). SABnzbd and NZBGet fetches now drop the Referer header when following a download redirect, and every redirect is checked against the same address rules as the original link, including when an outbound proxy is set.
- **More credentials are kept out of responses, logs and errors** (#2958). Jackett keys, tracker passkeys (`passkey`, `torrent_pass`, `authkey`, `rsskey`), tokens, signatures, the `r` key in newznab getnzb links and magnet tracker URLs are now stripped from download links, GUIDs and detail links in search, queue, pending and history results, and redacted from stored errors and the log export, the same way the indexer API key already was. Grabs still send the real values.
- **Webhook secrets no longer show up in notifier errors** (#2958). A failed notification logs only the webhook's host, so Discord, Slack, Telegram, Teams, Home Assistant, Apprise and ntfy tokens stay private.

### Changed
- **Grabbing an old search result after a restart now asks you to search again** (#2958). Search results are remembered for 24 hours; a grab from the web UI of one Bindery no longer remembers answers "this search result has expired, search again" instead of sending a link with its credentials removed. API key callers can still post their own download URL.
