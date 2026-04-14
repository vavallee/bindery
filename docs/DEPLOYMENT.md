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

## Binary

Pre-built archives are attached to every [Release](https://github.com/vavallee/bindery/releases) for:

| OS | Architectures | Runs on |
|----|---------------|---------|
| Linux | amd64, arm64, armv7, armv6 | x86_64 servers, Raspberry Pi 4 / 5 (64-bit), Pi 2 / 3 (32-bit), Pi Zero / 1 |
| macOS | amd64, arm64 | Intel Macs, Apple Silicon |
| Windows | amd64, arm64 | x86_64 desktops, Windows on ARM |

Pick the archive matching your platform, verify against `bindery_vX.Y.Z_checksums.txt`, extract, and run:

```bash
tar -xzf bindery_v0.5.0_linux_amd64.tar.gz
./bindery
```

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

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BINDERY_PORT` | `8787` | HTTP server port |
| `BINDERY_DB_PATH` | `/config/bindery.db` | SQLite database path |
| `BINDERY_DATA_DIR` | `/config` | Config directory (backups live here) |
| `BINDERY_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `BINDERY_API_KEY` | _(empty)_ | **Seed only.** Bootstraps the initial API key on first launch if set; after that the key lives in the database and can be regenerated from the UI. |
| `BINDERY_DOWNLOAD_DIR` | `/downloads` | Where SABnzbd places completed downloads |
| `BINDERY_LIBRARY_DIR` | `/books` | Destination for imported ebook files |
| `BINDERY_AUDIOBOOK_DIR` | falls back to `BINDERY_LIBRARY_DIR` | Destination for imported audiobook folders |
| `BINDERY_DOWNLOAD_PATH_REMAP` | _(empty)_ | Comma-separated `from:to` pairs rewriting paths reported by the download client. Longest-prefix match wins. |
| `BINDERY_PUID` | _(unset)_ | Sanity check — see [Running as a specific UID/GID](#running-as-a-specific-uidgid) |
| `BINDERY_PGID` | _(unset)_ | Sanity check — same as `BINDERY_PUID` for the primary GID |

## First-run setup

On first launch Bindery bootstraps itself — **no environment variables are required for auth.**

1. A random API key and session-signing secret are generated and stored in the SQLite database. Both are idempotent: generated once, reused on every subsequent boot.
2. The first page load redirects to `/setup`. Create the administrator account (username + password, 8-character minimum). Bindery is single-administrator; there is no "register" flow once this account exists.
3. After setup you're signed in automatically. Later visits redirect to `/login` if the session cookie has expired.

**Default auth mode is `enabled`.** Change it in **Settings → General → Security** if you want:

- `local-only` — skip auth for requests from private IPs (`10/8`, `172.16/12`, `192.168/16`, loopback, IPv6 ULA, link-local). Useful for home networks where the risk profile doesn't warrant a login wall.
- `disabled` — no auth at all. Only safe behind a trusted reverse proxy that handles authentication upstream.

## Upgrading

### From v0.5.x to v0.6.x

The auth overhaul landed in v0.6.0 is fully backwards-compatible on existing installs:

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
