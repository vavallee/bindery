# Calibre Bridge plugin mode

The `plugin` Calibre mode lets Bindery import books into a Calibre instance
running in a **separate container or pod** — no shared binary, no shared
volume, no `calibredb` on the Bindery container's PATH.

After each successful import, Bindery POSTs the book's absolute path to the
[Bindery Bridge Calibre plugin](https://github.com/vavallee/bindery-plugins)
over HTTP. The plugin runs a small HTTP server inside Calibre's process and
calls `db.new_api.add_books()` directly.

---

## Architecture

```
┌────────────────┐   POST /v1/books   ┌──────────────────────────────┐
│    Bindery     │ ─────────────────► │  Calibre + Bindery Bridge    │
│  (Go process)  │                    │  plugin (HTTP on :8099)       │
└────────────────┘                    └──────────────────────────────┘
        │                                        │
        │  shared NFS / volume                   │ db.new_api.add_books()
        ▼                                        ▼
   /media/BOOKS/                         Calibre metadata.db
```

Both containers must mount the same book library volume. Bindery moves the
file into the library first, then tells Calibre where it landed.

---

## Step 1 — Install the Bindery Bridge plugin

### Option A: manual (any Calibre deployment)

1. Download `calibre-bridge-vX.Y.Z.zip` from the
   [bindery-plugins releases](https://github.com/vavallee/bindery-plugins/releases).
2. In Calibre: **Preferences → Plugins → Load plugin from file** → select the `.zip`.
3. Restart Calibre.
4. **Preferences → Plugins → User plugins → Bindery Bridge → Customize** —
   set **Bind host**, **Listen port**, and **API key**.

### Option B: kubectl exec (headless / container)

When the Calibre GUI is running in a container and there's no desktop access
to upload files, install via `kubectl`:

```bash
# Download into the container
kubectl exec -n <namespace> deployment/<calibre-deployment> -- \
  wget -q -O /tmp/calibre-bridge.zip \
  https://github.com/vavallee/bindery-plugins/releases/download/v-calibre-bridge-X.Y.Z/calibre-bridge-vX.Y.Z.zip

# Install (calibre-customize is part of the linuxserver/calibre image)
kubectl exec -n <namespace> deployment/<calibre-deployment> -- \
  calibre-customize -a /tmp/calibre-bridge.zip

# Restart the pod to activate
kubectl rollout restart deployment/<calibre-deployment> -n <namespace>
```

After restart, the plugin HTTP server starts automatically in Calibre's
`genesis()` lifecycle hook.

### Option C: Kubernetes init-container (GitOps)

The `charts/calibre-plugin-installer` Helm chart in
[bindery-plugins](https://github.com/vavallee/bindery-plugins) injects an
init-container that downloads and installs the plugin before Calibre starts.
See [`docs/installation.md`](https://github.com/vavallee/bindery-plugins/blob/main/docs/installation.md)
in that repo for full setup.

---

## Step 2 — Configure the plugin

Open **Calibre → Preferences → Plugins → User plugins → Bindery Bridge → Customize**:

| Setting | Recommended value | Notes |
|---------|-------------------|-------|
| **Listen port** | `8099` | Any free port works |
| **Bind host** | `0.0.0.0` | Required for cross-pod access; `127.0.0.1` only works on bare-metal |
| **API key** | (generate one) | Use the **Generate** button in Bindery → Settings → Calibre, or run `openssl rand -hex 32` |

After saving, the dialog restarts the HTTP server in-place — no Calibre
restart needed.

### Verify the plugin is listening

```bash
kubectl exec -n <namespace> deployment/<calibre-deployment> -- \
  ss -tlnp | grep 8099
```

Expected: `LISTEN 0 5 0.0.0.0:8099`

---

## Step 3 — Expose the plugin port in Kubernetes

The Calibre Service must route port 8099 to the pod:

```yaml
# Add to your calibre Service spec.ports
- name: plugin
  port: 8099
  targetPort: 8099
  protocol: TCP
```

---

## Step 4 — Configure Bindery

In Bindery: **Settings → Calibre**:

| Field | Value |
|-------|-------|
| **Mode** | `plugin` |
| **Library path** | Path to the Calibre library root inside Bindery's container (e.g. `/media/BOOKS`) |
| **Plugin URL** | `http://calibre.<namespace>.svc.cluster.local:8099` |
| **Plugin API key** | The key you set in step 2 — paste it here, or click **Generate** to create a new one |

Click **Test connection** — it calls `GET /v1/health` and should return the
plugin version and Calibre version.

---

## Failure behaviour

| Scenario | Bindery behaviour |
|----------|------------------|
| Plugin returns `201` | Calibre book id stored on the Bindery book row |
| Plugin returns `409` (duplicate) | Existing Calibre id recorded; no error |
| Plugin returns `503` (library swap) | Retried once after 1 s; failure logged if still 503 |
| Plugin unreachable / timeout | Import is **not** rolled back — the book is in the library. Failure logged. Re-trigger via Settings → Calibre → Test or wait for next auto-import |

The import (file move) always succeeds or fails independently of the Calibre
notification. A Calibre glitch cannot orphan a downloaded book.

---

## Updating the plugin

### Manual

Repeat Step 1 Option A/B with the new `.zip`, then restart Calibre.

### kubectl exec

```bash
kubectl exec -n <namespace> deployment/<calibre-deployment> -- \
  wget -q -O /tmp/calibre-bridge.zip \
  https://github.com/vavallee/bindery-plugins/releases/download/v-calibre-bridge-X.Y.Z/calibre-bridge-vX.Y.Z.zip && \
kubectl exec -n <namespace> deployment/<calibre-deployment> -- \
  calibre-customize -a /tmp/calibre-bridge.zip && \
kubectl rollout restart deployment/<calibre-deployment> -n <namespace>
```

---

## HTTP API reference

See [`docs/protocol.md`](https://github.com/vavallee/bindery-plugins/blob/main/docs/protocol.md)
in the bindery-plugins repo for the full endpoint spec and versioning policy.
