# Deployment

This is the canonical reference for running Bindery in production. For a five-minute taste, the [README's Quick Start](../README.md#quick-start) is faster. For operational topics that change out-of-band with code (reverse proxy recipes, community-contributed setups, troubleshooting stories), see the [project wiki](https://github.com/vavallee/bindery/wiki).

## Docker

```bash
docker run -d \
  --name bindery \
  -p 8787:8787 \
  -v /path/to/config:/config \
  -v /path/to/books:/books \
  -v /path/to/downloads:/downloads \
  ghcr.io/vavallee/bindery:latest
```

**Image tracks:**

| Tag | Meaning |
|-----|---------|
| `:latest` | Most recent tagged release |
| `:vX.Y.Z` | Specific release — pin this for reproducible deploys |
| `:development` | Bleeding edge from the `development` branch |
| `:sha-<hash>` | Per-commit main-branch image — pin for rollback |
| `:dev-<hash>` | Per-commit development-branch image |

## Docker Compose

```yaml
services:
  bindery:
    image: ghcr.io/vavallee/bindery:latest
    container_name: bindery
    ports:
      - 8787:8787
    volumes:
      - ./config:/config
      - /media/books:/books
      - /media/downloads:/downloads
    environment:
      - BINDERY_LOG_LEVEL=info
    restart: unless-stopped
```

## Kubernetes (Helm)

```bash
helm install bindery charts/bindery \
  --set image.tag=latest \
  --set persistence.config.storageClass=longhorn \
  --set ingress.host=bindery.example.com
```

See [`charts/bindery/values.yaml`](../charts/bindery/values.yaml) for all configuration options.

### PodSecurityStandards namespace label

The chart's `securityContext` already satisfies Kubernetes' `restricted` policy (non-root user, read-only root filesystem, all capabilities dropped, `seccomp: RuntimeDefault`). To enforce this at the namespace level so that no chart override or future operator can weaken it, label your namespace before installing:

```bash
kubectl label namespace bindery \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest
```

`restricted` is the recommended setting. If you run a sidecar or init container that requires elevated privileges, use `baseline` instead — it blocks only the most critical misconfigurations (host namespaces, privileged mode) while permitting containers that need capabilities Bindery itself doesn't use.

The label belongs on the namespace, not in the chart, because Helm charts should not create or mutate namespaces they're installed into.

## Unraid (Community Applications)

Bindery ships a Community Applications template at
[`.github/unraid/bindery.xml`](../.github/unraid/bindery.xml). Once the
template is registered with the CA feed, Unraid users can install Bindery
from the **Apps** tab. Until then, paste the raw URL into Apps → "Click
here to add a missing application":

```
https://raw.githubusercontent.com/vavallee/bindery/main/.github/unraid/bindery.xml
```

Defaults the template provides: bridge networking, port `8787`,
`--user 99:100` (Unraid's `nobody:users`), and four mounts —
`/mnt/user/appdata/bindery → /config`, `/mnt/user/Books → /books`,
`/mnt/user/Audiobooks → /audiobooks` (optional), and
`/mnt/user/downloads/bindery → /downloads`. If you change `BINDERY_PUID`
or `BINDERY_PGID`, also change the matching half of `--user` in **Extra
Parameters** — the container fail-fast-validates the pair on startup.

## Binary

Pre-built archives are attached to every [Release](https://github.com/vavallee/bindery/releases) for:

| OS | Architectures | Runs on |
|----|---------------|---------|
| Linux | amd64, arm64, armv7, armv6 | x86_64 servers, Raspberry Pi 4 / 5 (64-bit), Pi 2 / 3 (32-bit), Pi Zero / 1 |
| macOS | amd64, arm64 | Intel Macs, Apple Silicon |
| Windows | amd64, arm64 | x86_64 desktops, Windows on ARM |

Pick the archive matching your platform, verify against `bindery_<version>_checksums.txt`, extract, and run.

### Linux

```bash
tar -xzf bindery_<version>_linux_amd64.tar.gz
./bindery
```

Database and backups land in `/config/` by default so the same binary slots into existing Docker / Helm deployments. Override with `BINDERY_DB_PATH` / `BINDERY_DATA_DIR` if you don't want `/config` (bare-metal users running as non-root will need to).

### macOS

```bash
tar -xzf bindery_<version>_darwin_arm64.tar.gz   # or _amd64 for Intel Macs
./bindery
```

On first run the database resolves to `~/Library/Application Support/Bindery/bindery.db`. The app respects `BINDERY_DB_PATH` / `BINDERY_DATA_DIR` if you want them elsewhere.

Gatekeeper may flag the unsigned binary; allow it in **System Settings → Privacy & Security** (the "bindery" entry shows up under "Security" after the first blocked launch).

### Windows

Unzip `bindery_<version>_windows_amd64.zip` (or `_arm64.zip` for Windows on ARM) and double-click `bindery.exe`. On first run the database resolves to `%APPDATA%\Bindery\bindery.db`.

If the console window closes instantly, open `cmd` and run the binary from there so the error message stays readable:

```cmd
cd %USERPROFILE%\Downloads\bindery_<version>_windows_amd64
bindery.exe
```

SmartScreen will warn about the unsigned binary on first launch — choose **More info → Run anyway**. Signed Windows builds are on the roadmap.

### Resolved paths logged at startup

Every launch emits a `"starting bindery"` JSON log line containing the resolved `dbPath` and `dataDir`. If the binary can't write to them, `db.Open`'s preflight will name the directory and the required UID so you can fix the permission without guesswork.

The frontend is embedded in the binary via `go:embed` — no separate static-file hosting needed.

## Running as a specific UID/GID

Bindery ships on a [distroless/static-debian12:nonroot](https://github.com/GoogleContainerTools/distroless) base. The image has no shell, no `gosu`, and no entrypoint hook — it cannot switch user at runtime the way LinuxServer.io images do. If you need the container to own files as your media-library user (e.g. `1000:1000`), launch it with that UID/GID directly.

### Docker

```bash
docker run -d \
  --user 1000:1000 \
  -e BINDERY_PUID=1000 \
  -e BINDERY_PGID=1000 \
  ...
  ghcr.io/vavallee/bindery:latest
```

### Docker Compose

```yaml
services:
  bindery:
    image: ghcr.io/vavallee/bindery:latest
    user: "1000:1000"
    environment:
      - BINDERY_PUID=1000
      - BINDERY_PGID=1000
```

### Kubernetes

```yaml
spec:
  template:
    spec:
      securityContext:
        runAsUser: 1000
        runAsGroup: 1000
        fsGroup: 1000         # makes mounted volumes owned by GID 1000
      containers:
        - name: bindery
          env:
            - { name: BINDERY_PUID, value: "1000" }
            - { name: BINDERY_PGID, value: "1000" }
```

### Sanity-check semantics

`BINDERY_PUID` / `BINDERY_PGID` are **sanity checks, not user switchers.** If you set them but forget the `--user` / `runAsUser` side, Bindery fails fast at startup with a log line that shows exactly what flag was missing — replacing the usual silent `permission denied` on `/config` or the library mount. Leaving both unset skips the check entirely (Bindery runs as the default non-root UID `65532` from the distroless base).

## Path remapping (multi-container / multi-pod setups)

When Bindery and your download client run in **separate containers**, they typically mount the same storage volume at different paths. Bindery needs to read the files the download client just completed, but the path the client reports (e.g. `/downloads/complete/My.Book`) doesn't exist inside Bindery's container.

Set a download-client path remap in **Settings → Download clients** or set the global `BINDERY_DOWNLOAD_PATH_REMAP` fallback to a comma-separated list of `from:to` pairs. Bindery applies a longest-prefix match to every path the download client reports, replacing the matched prefix before it tries to access the file. A per-client remap takes precedence when it matches the reported path; the global env var still applies as a fallback.

Per-client remaps are stored on each download client, so separate qBittorrent / SABnzbd / NZBGet instances can map different mount points. Existing download clients keep an empty remap after upgrade, which preserves the previous global-only behavior until you add a client-specific value.

For a per-client remap, open **Settings → Download clients**, edit the client, and set **Download client path remap**. The left side is the path the client reports; the right side is the path Bindery can read. For qBittorrent this normally means mapping the qBittorrent category save path or torrent content path to Bindery's download mount. Example: if qBittorrent reports `/downloads/books/My.Book` and Bindery sees that same folder as `/media/books/My.Book`, set `/downloads:/media/books`.

**Common scenario — SABnzbd or qBittorrent and Bindery on the same NAS storage, different mount points:**

| Container | NAS path | Mount point |
|-----------|----------|-------------|
| Download client | `/volume1/MEDIA` | `/downloads` |
| Bindery | `/volume1/MEDIA` | `/media` |

The download client reports `/downloads/complete/My.Book`; Bindery remaps to `/media/complete/My.Book`.

### Docker Compose

```yaml
services:
  sabnzbd:
    image: lscr.io/linuxserver/sabnzbd:latest
    volumes:
      - /mnt/media:/downloads        # NAS/share mounted at /downloads

  bindery:
    image: ghcr.io/vavallee/bindery:latest
    volumes:
      - /mnt/media:/media            # same share, different mount point
    environment:
      - BINDERY_DOWNLOAD_PATH_REMAP=/downloads:/media
```

### Kubernetes (Helm `values.yaml`)

```yaml
env:
  BINDERY_DOWNLOAD_PATH_REMAP: /downloads:/media

nfs:
  enabled: true
  server: 192.168.1.4
  path: /volume1/MEDIA
  mountPath: /media
```

Multiple remaps are separated by commas: `BINDERY_DOWNLOAD_PATH_REMAP=/sab/complete:/media/complete,/qbit:/media/qbit`. Longest prefix wins, so more-specific rules take precedence over shorter ones.

For qBittorrent, Bindery also sends a save path when submitting torrent grabs. If a per-client remap is configured, Bindery inversely maps `BINDERY_DOWNLOAD_DIR` or `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` back to qBittorrent's mount point before submitting the torrent. The Settings page checks the configured qBittorrent category and warns when its save path does not map to Bindery's expected download folder.

After fixing a path or category mismatch, use **Queue → Retry import** on an `importFailed` item. This is also the right action after manually moving the completed torrent in qBittorrent to a path Bindery can read. Bindery resets the import retry counter and imports from the existing completed download, so the release does not need to be grabbed or downloaded again.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BINDERY_PORT` | `8787` | HTTP server port |
| `BINDERY_URL_BASE` | _(empty)_ | URL path prefix when hosting Bindery under a reverse-proxy subpath (e.g. `/bindery`). Accepts a bare path or full URL — only the path component is used. No trailing slash needed. See the [Reverse-proxy & SSO wiki](https://github.com/vavallee/bindery/wiki/Reverse-proxy-and-SSO) for Nginx / Caddy / Traefik examples. |
| `BINDERY_DB_PATH` | `/config/bindery.db` on Linux; `%APPDATA%\Bindery\bindery.db` on Windows; `~/Library/Application Support/Bindery/bindery.db` on macOS | SQLite database path |
| `BINDERY_DATA_DIR` | `/config` on Linux; `%APPDATA%\Bindery` on Windows; `~/Library/Application Support/Bindery` on macOS | Config directory (backups live here) |
| `BINDERY_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `BINDERY_API_KEY` | _(empty)_ | **Seed only.** Bootstraps the initial API key on first launch if set; after that the key lives in the database and can be regenerated from the UI. |
| `BINDERY_DOWNLOAD_DIR` | `/downloads` | Where the download client places completed downloads |
| `BINDERY_AUDIOBOOK_DOWNLOAD_DIR` | falls back to `BINDERY_DOWNLOAD_DIR` | Separate watch folder for audiobook downloads; set this when your download client routes audiobook grabs to a dedicated category/path |
| `BINDERY_LIBRARY_DIR` | `/books` | Destination for imported ebook files |
| `BINDERY_AUDIOBOOK_DIR` | falls back to `BINDERY_LIBRARY_DIR` | Destination for imported audiobook folders |
| `BINDERY_ENHANCED_HARDCOVER_API` | `true` | Set to `false` to disable token-backed Hardcover series search, linking, catalog diffs, and missing-book fill even when an admin enables the feature in Settings. |
| `BINDERY_DOWNLOAD_PATH_REMAP` | _(empty)_ | Global comma-separated `from:to` pairs rewriting paths reported by download clients into paths Bindery can access. Per-client path remaps in Settings take precedence when they match. Longest-prefix match wins. See [Path remapping](#path-remapping-multi-container--multi-pod-setups). |
| `BINDERY_PUID` | _(unset)_ | Sanity check — see [Running as a specific UID/GID](#running-as-a-specific-uidgid) |
| `BINDERY_PGID` | _(unset)_ | Sanity check — same as `BINDERY_PUID` for the primary GID |
| `BINDERY_COOKIE_SECURE` | `auto` | Session cookie `Secure` flag policy. `auto` (default) flips the flag on when TLS is detected directly or via `X-Forwarded-Proto: https`; `always` forces it on (use when your reverse proxy doesn't forward the header); `never` forces it off (legacy plain-HTTP installs). |
| `BINDERY_NOTIFICATIONS_ALLOW_PRIVATE` | _(unset)_ | Set to `1` to flip outbound webhook SSRF policy from Strict to LAN, allowing RFC1918 targets. Use when ntfy / Home Assistant / Gotify live on your private network. Loopback, link-local, and cloud-metadata endpoints stay blocked. |
| `BINDERY_CONTACT` | _(falls back to project URL)_ | Contact pointer Bindery advertises in its `User-Agent` header. Accepts a `mailto:` URI, a bare email address (auto-prefixed with `mailto:`), or an `http(s)://` URL. **Set this when OpenLibrary author/title searches return HTTP 403** — OpenLibrary rate-limits per-UA and the default shared URL causes the whole Bindery fleet to count as one client. A per-instance contact differentiates each install and satisfies OpenLibrary's API policy (see [#848](https://github.com/vavallee/bindery/issues/848)). Example: `BINDERY_CONTACT=you@example.org`. Bindery never connects to the address; it goes only into the header. |
| `BINDERY_RATE_LIMIT_MAX_FAILURES` | `5` | Maximum failed login attempts per IP before the account is locked for the rate-limit window. |
| `BINDERY_RATE_LIMIT_WINDOW_MINUTES` | `15` | Duration in minutes of the per-IP login rate-limit window. After the window expires the failure counter resets. |
| `BINDERY_SHUTDOWN_GRACE` | `30` | Seconds to drain in-flight HTTP requests after receiving SIGTERM or SIGINT before the process exits. Increase if your load balancer / Kubernetes sends long-lived SSE or WebSocket connections. |
| `BINDERY_ENFORCE_TENANCY` | _(off)_ | Set to `true`/`1` to enforce per-user data isolation: each user sees only their own authors, books, profiles, and root folders, and the join-scoped queue / history / pending / OPDS feeds are scoped to the requesting user. **Defaults off**, in which case every authenticated user shares one library view (single-user behaviour). Admin-only configuration gating applies regardless of this flag. See [multi-user.md](multi-user.md). |
| `BINDERY_LOG_RETENTION_DAYS` | `14` | Days to retain persisted log entries in the SQLite log store before they are pruned. |
| `BINDERY_TRUSTED_PROXY` | _(empty)_ | Comma-separated IP/CIDR list of reverse proxies trusted to set `X-Forwarded-*`. Bindery resolves the real client IP (for local-only auth and the per-IP login rate-limiter) by walking the `X-Forwarded-For` chain and only trusting hops in this list; it never trusts a client-supplied leftmost entry. An entry like `0.0.0.0/0` trusts every peer and effectively disables per-IP decisions. **Required** when proxy auth mode is active — Bindery refuses to start without it. |
| `BINDERY_TELEMETRY_DISABLED` | _(unset)_ | Set to `true` to opt out of the daily anonymous telemetry ping before any DB setting exists (e.g. on first boot). Equivalent to `telemetry.enabled: false` in **Settings → General**, but takes effect before the first ping fires. |

## Service URLs and the SSRF policy

Outbound URLs are validated against an SSRF policy. Two trust levels apply:

- **Admin-typed service URLs** — download clients (qBittorrent/Transmission/Deluge/SABnzbd/NZBGet), indexers, Prowlarr, the Audiobookshelf base URL, and the Calibre plugin URL. These **allow loopback** (`127.0.0.1`, `::1`) and RFC1918 LAN ranges (`10/8`, `172.16/12`, `192.168/16`). So `http://127.0.0.1:9696` or `http://localhost:50155` work when the service runs on the same host (including `network_mode: host`, or a companion bound only to loopback). Link-local (`169.254/16`, `fe80::/10`) and cloud-metadata endpoints (e.g. `169.254.169.254`) are always blocked. These endpoints are admin-only and CSRF-gated, so loopback is the operator's call.

- **Untrusted / outbound URLs** — proxied cover images (URLs that come from metadata providers and book data) and outbound notification webhooks. These keep blocking loopback, link-local, and cloud-metadata. Webhooks additionally block RFC1918 unless `BINDERY_NOTIFICATIONS_ALLOW_PRIVATE=true`.

If a same-host service still isn't reachable, the usual cause is that the service is bound to an interface your URL doesn't match (for example SABnzbd listening only on `127.0.0.1` while you used the LAN IP, or vice versa). Either point Bindery at the interface the service actually listens on, or set the service to listen on `0.0.0.0`.

## First-run setup

On first launch Bindery bootstraps itself — **no environment variables are required for auth.**

1. A random API key and session-signing secret are generated and stored in the SQLite database. Both are idempotent: generated once, reused on every subsequent boot.
2. The first page load redirects to `/setup`. Create the administrator account (username + password, 8-character minimum). Bindery is single-administrator; there is no "register" flow once this account exists.
3. After setup you're signed in automatically. Later visits redirect to `/login` if the session cookie has expired.

**Default auth mode is `enabled`.** Change it in **Settings → General → Security** if you want:

- `local-only` — skip auth for requests from private IPs (`10/8`, `172.16/12`, `192.168/16`, loopback, IPv6 ULA, link-local). Useful for home networks where the risk profile doesn't warrant a login wall.
- `disabled` — no auth at all. Only safe behind a trusted reverse proxy that handles authentication upstream.

## Upgrading

### ABS import deployment note

**Schema:** ABS import uses migrations `029` through `033`. They create five ABS tables: `abs_import_runs`, `abs_provenance`, `abs_metadata_conflicts`, `abs_import_run_entities`, and `abs_review_queue`. Migration `031` also adds `dry_run`, `source_config_json`, and `checkpoint_json` to `abs_import_runs`; migration `033` is currently a no-op compatibility migration. Take a normal SQLite backup before upgrading, then let Bindery apply the migrations on startup.

**Outbound ABS requests:** ABS probes and imports send `User-Agent: bindery/<version>` to the configured ABS server. Development or unversioned builds use `bindery/dev`.

### Enhanced Hardcover series data deployment note

**Schema:** enhanced series data uses migration `035`, which creates `series_hardcover_links` and backfills links for existing series whose foreign ID already points at Hardcover. Take a normal SQLite backup before upgrading, then let Bindery apply the migration on startup.

**Feature flag:** token-backed Hardcover series search, manual/automatic series linking, catalog diffs, and missing-book fill are available by default at deployment time, but still require a saved Hardcover API token in **Settings -> General** and the enhanced Hardcover series toggle in the same settings section. Set `BINDERY_ENHANCED_HARDCOVER_API=false` only when you need to disable the enhanced endpoints and hide the UI controls for an entire deployment. Existing local series data keeps working when the feature is disabled.

**Operational note:** the enhanced fill action can create wanted/monitored book rows from the linked Hardcover catalog and immediately queue indexer searches. Make sure outbound HTTPS to Hardcover and your configured indexers is allowed before enabling it for production users.

### From v1.9.x to v1.10.0

**Schema:** migration `040` adds `path_remap TEXT` to `download_clients`. Non-destructive; no action required.

**Docker/container networking — qBittorrent and NZBGet** — If you previously saw qBittorrent or NZBGet silently fail to grab torrent/NZB files when using Prowlarr or other indexers with Docker-internal hostnames (e.g. `prowlarr:9696`), this is fixed. Bindery now fetches the content itself and forwards it directly to the download client — qBittorrent and NZBGet never need to resolve the indexer URL.

**Graceful shutdown** — `BINDERY_SHUTDOWN_GRACE` (default `30s`) controls how long the server drains in-flight requests after SIGTERM. `kubectl rollout restart` no longer drops in-flight requests.

**Log level UI toggle** — The log level toggle in Settings → System now immediately affects the log viewer (previously only `BINDERY_LOG_LEVEL` at startup worked).

### From v0.11.x to v0.12.0 (security posture)

**Schema:** no changes. Drop-in binary or image replacement is safe.

**Auth hashing note.** If a very old install predates argon2id (unlikely — argon2id has been the only code path since v0.6.0), log in and change your password to trigger a rehash. All current installs are already on argon2id.

**New env vars (both optional, documented above):**

- `BINDERY_COOKIE_SECURE` — defaults to `auto`. Existing deployments behind Traefik / Caddy / nginx that forward `X-Forwarded-Proto: https` need no action; the cookie now correctly sets `Secure`. Plain-HTTP homelab users on a LAN may need `BINDERY_COOKIE_SECURE=never` if browsers start rejecting the cookie.
- `BINDERY_NOTIFICATIONS_ALLOW_PRIVATE=1` — required if you notify an on-LAN ntfy / Home Assistant / Gotify. Without it, webhook URLs resolving to RFC1918 are rejected at submit time.

**Helm chart (optional but recommended).** New `auth.existingSecret` / `auth.apiKey` value keys render the API key via a Kubernetes Secret instead of a plain env value. Existing releases keep working with the old `env.BINDERY_API_KEY` pattern, but migrating is a one-line upgrade:

```yaml
auth:
  existingSecret: my-bindery-secret  # kubectl create secret generic my-bindery-secret --from-literal=apiKey=...
```

**Response headers.** Every response now sets CSP, `X-Frame-Options: DENY`, `Referrer-Policy`, and — when TLS is in play — HSTS. If you previously embedded the Bindery UI in an `<iframe>`, `X-Frame-Options: DENY` will block it. No such usage is supported, but it's the most likely breakage vector.

### From v0.10.x to v0.11.0

**Schema:** no changes. Drop-in binary or image replacement is safe.

**New features, no action required:**

- **In-process log viewer** at Settings → Logs (last 1 000 entries, colour-coded, DEBUG toggle without restart).
- **UI localization** — English / French / German / Dutch. Auto-detects from `Accept-Language`; override in Settings → General → Language.
- **Root folders** — configure multiple library roots under Settings → Root Folders. Existing single-root installs keep using `BINDERY_LIBRARY_DIR` as the default.
- **Language propagation** — per-author metadata-profile language filters now ride into indexer queries.

### From v0.9.x to v0.10.0 (dual-format)

**Schema:** migration `012_dual_format.sql` adds `ebook_file_path`, `audiobook_file_path`, and `media_type` columns to `books` and copies existing `file_path` data into `ebook_file_path`. Non-destructive; `file_path` is kept for one release as a fallback.

**Existing single-format downloads** show up in the correct format slot after the migration runs on startup — no manual action needed. A book with an imported ebook will not re-queue the ebook on the next sweep but will still search for a missing audiobook.

### From v0.8.x to v0.9.0 (Calibre modes, OPDS, auto-grab kill-switch)

**Schema:** three additive migrations (`010_calibre_sync.sql`, `011_calibre_mode.sql`, new `editions` table). Drop-in safe.

**Calibre mode defaults to Off.** Existing installs that used the v0.8.0 `calibre.enabled=true` boolean are automatically shown as **calibredb CLI** mode in the UI via a back-compat fallback — no re-configuration needed.

**Auto-grab defaults to On** (existing behaviour). Toggle it off in Settings → General → Auto-grab if you prefer manual grabs, or when bulk-adding large author lists that would otherwise fire thousands of simultaneous indexer queries.

**OPDS** is available at `/opds/` — browse and download your library from KOReader / Moon+ Reader / Aldiko. Authenticates via Bindery username + password over HTTP Basic, or via `X-Api-Key` / `?apikey=` query parameter for scripts. See the [OPDS wiki page](https://github.com/vavallee/bindery/wiki/OPDS).

**Behaviour change — catalogue fetch decoupled from auto-grab.** Unchecking "Auto-grab books on add" no longer silently prevents the book catalogue from loading. The full book list is always fetched; the checkbox only controls whether Bindery immediately sends results to the download client.

### From v0.7.x to v0.8.0

**Schema:** two additive migrations (`008_calibre.sql`, `009_author_aliases.sql`). Drop-in binary or image replacement is safe — existing data is untouched.

**Calibre (optional, off by default).** If you want the new `calibredb` post-import hook, you need the `calibredb` binary reachable from the Bindery process:

- The distroless official image does **not** ship `calibredb`. Either bind-mount a calibre install into the container or run Bindery outside the distroless image until a `bindery-calibre` variant lands.
- Enable via Settings → General → Calibre → set library path + binary path → Test connection.
- Existing imports continue to work unchanged while the toggle is off.

**Author aliases — no auto-merge.** Duplicate author rows that existed before the upgrade are not merged automatically. Use the new **Merge authors** modal on the Authors page (or per-author Merge button) to reunite them — the decision needs a human eye.

### From v0.6.x to v0.7.0

**Schema:** no changes. Drop-in binary or image replacement is safe.

**Behavior change — auto-search on add is on by default.** Adding a new author or flipping a book to `wanted` now immediately fires an indexer search. Previously the scheduler waited up to 12 hours. If this is unwanted (e.g. you want to batch-add many authors before any searches fire), uncheck the new **Start search for books on add** box in the Add Author modal. Books that transition to `wanted` via API always trigger a search; a `search_on_status_change` setting will be added later if opt-out is requested — file an issue if you need it.

**Backfill existing libraries (series data):** The `series` and `series_books` tables have existed since v0.1 but were never populated. Authors added before this release therefore have no series rows. After upgrading, run the one-shot reconcile command to backfill series data from OpenLibrary:

```bash
# Docker
docker exec bindery /bindery reconcile-series

# Binary / bare-metal
./bindery reconcile-series
```

The command prints a JSON summary `{"linked":<n>,"skipped":<n>}` and exits. It is idempotent — safe to run more than once. Rate-limiting on OpenLibrary's side means large libraries (hundreds of authors) may take a few minutes; run it in a `screen` or `tmux` session if needed.

After the backfill, the **Series** page in the UI will show all series that OpenLibrary associates with your authors' books.

### From v0.6.x to v0.6.4

No migration steps required. Drop-in replacement.

After upgrading, open **Settings → Indexers**, edit each indexer, and verify the categories field shows the correct IDs for that indexer. All existing indexers retain their previous category list (default `7020`). For indexers with non-standard category IDs add them now — for example SceneNZBs: `7020, 7120, 3030, 3130`.

### From v0.6.x to v0.6.3

No migration steps required. This is a bug-fix release.

- **Standalone binary UI fix** — if you were running v0.6.0–v0.6.2 from a downloaded archive and saw only `.gitkeep` at `http://localhost:8787`, this is fixed. Re-download the v0.6.3 archive for your platform.
- **Protocol routing** — torznab (torrent) indexers now route grabs to qBittorrent; newznab (Usenet) indexers route to SABnzbd. If you previously had torrent grabs fail silently, remove any failed queue entries and re-grab.
- **qBittorrent credential fields** — the Settings form now shows Username/Password fields. Existing qBittorrent clients already have the correct values stored; the UI change is cosmetic.

### From v0.5.x to v0.6.x

The auth overhaul (first installable in v0.6.1; `v0.6.0` tag's release binaries never built) is fully backwards-compatible on existing installs:

- The new `users` table and `auth.*` settings are added by an additive migration. No manual step required.
- If you had `BINDERY_API_KEY` set, it **seeds** the new key on first boot so existing integrations keep working. After that the key lives in the database; the env var is inert and can be removed. Leaving it set won't hurt but it no longer drives runtime behaviour.
- Your next visit to the UI will redirect to `/setup` to create the admin account.
- If you rely on calling the API without an API key (because `BINDERY_API_KEY` was unset in v0.5), switch to `local-only` mode after setup to preserve that behaviour for in-cluster traffic, or update your callers to send `X-Api-Key`.

### Backup before upgrade

Always snapshot the SQLite database before a minor-version bump:

```bash
curl -X POST -H "X-Api-Key: ..." http://bindery:8787/api/v1/backup
```

or via the UI: **Settings → General → Backup → Create backup.** Backups land in `$BINDERY_DATA_DIR` (default `/config`).

## See also

- [Wiki: Reverse-proxy & SSO setups](https://github.com/vavallee/bindery/wiki/Reverse-proxy-and-SSO) — Traefik / Caddy / Nginx / Authelia / Authentik recipes
- [Wiki: Troubleshooting](https://github.com/vavallee/bindery/wiki/Troubleshooting) — permission-denied, path-remap, import failures
- [Wiki: Migrating from Readarr](https://github.com/vavallee/bindery/wiki/Migrating-from-Readarr) — step-by-step with known failure modes
