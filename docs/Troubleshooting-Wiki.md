# Troubleshooting

Solutions to recurring problems, organised by symptom. Add new entries here as patterns come up in support.

## A grabbed book never imports

The book shows **grabbed** in History and reaches 100% in your download client, but Bindery never imports it. Usually one of two causes.

### qBittorrent 5.x with an old Bindery

qBittorrent 5.x changed the reply it sends when a torrent is added: 4.x answered with the plain text `Ok.`, 5.x answers with a JSON object. Bindery **before 1.12.1** only understood `Ok.`, so it treats every add to a qBittorrent 5.x client as failed even though the torrent was accepted and downloads fine.

You may see `add torrent failed: {"added_torrent_ids":...}` or `failed to send to downloader`. Because Bindery believes the hand-off failed, it never records the download, so it is never imported and no `importFailed` event appears.

**Fix:** upgrade to Bindery **1.12.1 or later** (the current release is recommended). Bindery 1.11.0 and earlier cannot complete torrent grabs against qBittorrent 5.x at all — it is a hard incompatibility. After upgrading, re-grab anything that was stuck.

### Bindery cannot read the completed files

If Bindery and the download client see the storage at different paths (different container mounts), Bindery cannot find the finished download. This usually surfaces as `importFailed` in the Queue.

**Fix:** set a download-client path remap in **Settings → Download clients**, then use **Queue → Retry import**. See [Path remapping](./DEPLOYMENT.md#path-remapping-multi-container--multi-pod-setups) in `DEPLOYMENT.md`.

## "Could not reach the metadata provider" / OpenLibrary timeout

```
metadata provider unavailable: search authors: Get "https://openlibrary.org/..."
context deadline exceeded (Client.Timeout exceeded while awaiting headers)
```

Bindery waited 15 seconds for OpenLibrary (run by the Internet Archive) and got nothing back. Common causes:

- **VPN or datacenter IP** — the Internet Archive throttles or blocks many shared VPN and hosting IP ranges.
- **OpenLibrary outage** — the Internet Archive has intermittent downtime.

Bindery's primary metadata provider is OpenLibrary or DNB (the German national library). There is currently no English alternative as the primary provider, so the fix is to make OpenLibrary reachable rather than to switch provider.

**Fixes:**

- Behind a VPN: split-tunnel `openlibrary.org` out of the VPN. Metadata lookups do not need VPN protection — only torrent traffic does — so a paid dedicated IP is not required. Switching to a different VPN exit location also often helps, since some exit IPs are blocked and others are not.
- Not on a VPN: retry later, and check the status of `openlibrary.org` / `archive.org`.
