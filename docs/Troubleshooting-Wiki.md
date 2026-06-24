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

## A book is on hardcover.app but doesn't show up in Add Book / Add Author search

The provider dropdown in `Settings → General` only offers **OpenLibrary** or **DNB** for the *primary* provider. Hardcover cannot be set as the primary, and it does not need to be: it always runs as a **search enricher**. Add Book and Add Author fan the query out to the primary provider **plus** Hardcover (and Google Books, if an API key is set), then merge in any titles the primary didn't return. Books that only exist on hardcover.app are exactly what that path is meant to surface.

The catch is that **Hardcover's GraphQL API requires an API token for every query, including search** — an unauthenticated request returns `{"error":"Unable to verify token"}`. Bindery skips a provider that errors rather than failing the whole search, so without a token Hardcover contributes nothing silently and you only see OpenLibrary / DNB results.

**Fix:** add a Hardcover API token in `Settings → General` (the same token used for [Enhanced Hardcover Series](./Hardcover-Series-Wiki.md) and wishlist features), then re-run the search. Hardcover-only titles should appear in the merged results.

If results still don't appear with a token saved, confirm the instance has outbound HTTPS access to `api.hardcover.app` and that the token is valid (a bad token produces the same "Unable to verify token" error, which is logged and skipped).

## Why is the metadata button on some authors but not others?

The metadata button on an author's page only appears when Bindery thinks the author's record could be improved, so you'll see it on some authors and not others. Two cases show it:

- **"Link metadata"** — the author isn't linked to a metadata provider yet, or was created from an **Audiobookshelf / Calibre import** (those use `abs:` / `calibre:` foreign IDs). The button lets you attach a real provider record.
- **"Find better metadata"** — the author *is* linked, but the stored record is **sparse**: no description, no image, no disambiguation, and no ratings. The button searches the providers for a richer match to relink to.

An author that already has a filled-in record (a description, an image, ratings) hides the button, because there's nothing obviously better to fetch. So a missing button means that author already has good metadata. If an author looks well populated but still shows the button, the stored description/image/ratings are likely empty even though the page renders other fields — relink and pick the best match to fill them in.
