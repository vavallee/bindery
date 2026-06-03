# Quickstart: zero to first download

This is the in-repo walkthrough for getting a fresh Bindery install from
nothing to its first grabbed book. It assumes Docker; the same UI steps apply
to the binary and Helm installs covered in
[docs/DEPLOYMENT.md](DEPLOYMENT.md).

For deeper, out-of-band recipes (specific indexers, reverse proxies, SSO), see
the [project wiki](https://github.com/vavallee/bindery/wiki).

---

## 1. Run Bindery

Use the `docker run` or Compose snippet from the
[README Quick Start](../README.md#quick-start) (also in
[DEPLOYMENT.md → Docker](DEPLOYMENT.md#docker)). The essentials:

- **Port `8787`** is the web UI, API, and OPDS catalogue.
- **Three volumes** must be mounted:
  - `/config` — SQLite database, image cache, and backups. Must persist.
  - `/books` — where imported ebooks land (`BINDERY_LIBRARY_DIR`).
  - `/downloads` — where your download client writes completed jobs
    (`BINDERY_DOWNLOAD_DIR`).

```bash
docker run -d \
  --name bindery \
  -p 8787:8787 \
  -v /path/to/config:/config \
  -v /path/to/books:/books \
  -v /path/to/downloads:/downloads \
  ghcr.io/vavallee/bindery:latest
```

No environment variables are required to start — auth bootstraps itself on
first run (see [DEPLOYMENT.md → First-run setup](DEPLOYMENT.md#first-run-setup)).

## 2. First login (create the admin)

Open <http://localhost:8787>. The first page load redirects to **`/setup`**.
Create the administrator account (username + password, **8-character
minimum**). Bindery is single-administrator: there is no "register" flow once
this account exists. After setup you are signed in automatically.

## 3. Add an indexer

Go to **Settings → Indexers**. You can add a direct Newznab/Torznab indexer or
point Bindery at a Prowlarr instance.

**Direct Newznab/Torznab** — click **+ Add Indexer** and fill in:

- **Indexer Type** — `Newznab (Usenet)` or `Torznab (Torrent)`. This sets the
  indexer's *protocol* and decides which download client it can route to (see
  step 4).
- **URL** — a base Newznab URL (e.g. `https://api.nzbgeek.info`) or a full
  Torznab endpoint (e.g. `http://prowlarr:9696/1/api`).
- **API Key** — from the indexer.
- **Categories** — comma-separated Newznab category IDs. Default `7020`
  (eBooks); `3030` is Audiobooks. Add custom IDs for indexers with
  non-standard categories.

**Prowlarr** — under **Settings → Indexers → Add Prowlarr**, give the base URL
(e.g. `http://prowlarr:9696`) and API key, then **Sync now** to pull its
configured indexers in.

> **Gotcha — `localhost` / `127.0.0.1` indexer URLs are rejected.**
> Indexer and Prowlarr URLs are validated against an SSRF policy that **blocks
> loopback** (`127.0.0.0/8`, `::1`), link-local (`169.254.0.0/16`), and
> cloud-metadata endpoints. This is intentional: a confused-deputy request to
> a loopback URL would let any logged-in user probe services running locally
> on the Bindery host, and there is **no env-var escape hatch**. RFC1918
> ranges (`10/8`, `172.16/12`, `192.168/16`) are allowed, so use:
>
> - **Same Docker network** → the service name: `http://prowlarr:9696`.
> - **Same host, different containers** → the host's LAN IP:
>   `http://192.168.x.y:9696`.
> - **Bare-metal** → the host's LAN IP, or have the indexer listen on a
>   non-loopback interface.
>
> Full detail: [DEPLOYMENT.md → Indexer / Prowlarr URLs](DEPLOYMENT.md#indexer--prowlarr-urls).

Use the **Test** button on the saved indexer to confirm it connects and
returns categories.

## 4. Add a download client

Go to **Settings → Download Clients → Add**. Pick the **Client Type**:

| Indexer protocol | Compatible download clients |
|------------------|-----------------------------|
| Newznab (Usenet) | SABnzbd, NZBGet |
| Torznab (Torrent) | qBittorrent, Transmission, Deluge |

> **The client's protocol must match your indexer.** A Usenet/NZB indexer
> needs an NZB client (SABnzbd or NZBGet); a torrent indexer needs a torrent
> client (qBittorrent, Transmission, or Deluge). If they don't match, grabs go
> nowhere. (Torznab indexers route grabs to the torrent client; newznab
> indexers route to the NZB client.)

Fill in the connection (host + port), credentials (API key for SABnzbd;
username/password for the others), and the **Category / Label** (default
`books`).

> **The category must already exist in the client.** Bindery does not create
> it for you. Create the matching category/label in SABnzbd / NZBGet /
> qBittorrent / Deluge first, or grabs will be misfiled or rejected.
> (Transmission uses a download-directory path instead of a category.)

Use **Test** to confirm the connection.

> **Same-host clients:** the loopback rule in step 3 is an *indexer-URL*
> validation, not a download-client one. But if Bindery runs in a separate
> container, a download-client host of `127.0.0.1` still points at Bindery's
> own container, not the client. Use the service name or LAN IP here too.
> When Bindery and the client mount the same storage at different paths, set a
> [download-client path remap](DEPLOYMENT.md#path-remapping-multi-container--multi-pod-setups).

## 5. Add an author and grab a book

Go to **Authors → Add Author**. Search by name (results come from OpenLibrary /
Hardcover / DNB), pick the author, choose a **Monitor mode** (default monitors
all books), and add them. Adding an author **populates their catalogue** —
Bindery fetches the full book list regardless of auto-grab.

Monitored books that are still missing become **wanted**. With "Search for
books on add" enabled (the default), Bindery immediately queries your indexers
and hands matching releases to the download client. To do it by hand, open the
**Wanted** page, hit **Search** on a book, and **Grab** a result. The
completed download is imported into `/books` with metadata.

---

## Troubleshooting

**Searches return nothing, or OpenLibrary returns HTTP 403.**
OpenLibrary rate-limits per `User-Agent`, and Bindery's default UA points at
the shared project URL — so every install counts as one client and can get
throttled. Set **`BINDERY_CONTACT`** to a per-instance contact (a bare email,
a `mailto:` URI, or an `http(s)://` URL); Bindery embeds it in the `User-Agent`
header to differentiate your install and satisfy OpenLibrary's API policy.
Bindery never connects to the address — it goes only into the header.

```bash
-e BINDERY_CONTACT=you@example.org
```

See [DEPLOYMENT.md → `BINDERY_CONTACT`](DEPLOYMENT.md#environment-variables)
and [#848](https://github.com/vavallee/bindery/issues/848).

**It's running but nothing downloads.**
Check that **both** an indexer and a download client exist and are enabled, and
that their **protocols match** (step 4): a torrent indexer with only an NZB
client configured — or vice versa — has nowhere to send grabs. Use the **Test**
button on each to confirm they connect, and verify the client's category
exists. If a download completes but never imports, see
[DEPLOYMENT.md → Path remapping](DEPLOYMENT.md#path-remapping-multi-container--multi-pod-setups)
and the [Troubleshooting wiki](https://github.com/vavallee/bindery/wiki/Troubleshooting).

---

## Where to go next

- [DEPLOYMENT.md](DEPLOYMENT.md) — UID/GID, path remapping, env-var reference,
  upgrades.
- [Wiki: Indexer & download-client recipes](https://github.com/vavallee/bindery/wiki/Indexer-and-downloader-recipes)
  — NZBGeek / DrunkenSlug / Prowlarr / Jackett / SAB / qBit tips.
- [docs/multi-user.md](multi-user.md) — roles and per-user libraries.
