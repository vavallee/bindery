# Reverse-Proxy SSO Authentication

Bindery v0.23.0 adds a `proxy` auth mode that delegates identity to an upstream reverse proxy — Authelia, Authentik, Keycloak, Google, GitHub, or any system that sets a trusted identity header.

> **Security warning:** Proxy mode is only safe if your Bindery instance is not directly reachable from untrusted networks. Any client that can reach Bindery and forge `X-Forwarded-User` without going through your proxy can authenticate as any user. Use firewall rules or network policy to enforce this.

## How it works

When `mode=proxy`, Bindery reads an identity header (default `X-Forwarded-User`) on every request. If the request arrives from a trusted proxy IP and the header is present, Bindery resolves or auto-provisions a user by that username and issues a session.

**Bindery refuses to start in proxy mode if `BINDERY_TRUSTED_PROXY` is empty** — this is intentional. The startup log emits the trusted CIDR list so you can verify it in `kubectl logs` or `docker logs`.

## Prerequisites

- Your reverse proxy is the sole path into Bindery from untrusted networks.
- You know the proxy container/pod IP or CIDR (`BINDERY_TRUSTED_PROXY`).

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BINDERY_TRUSTED_PROXY` | _(required in proxy mode)_ | Comma-separated CIDRs or IPs of trusted upstream proxies (e.g. `10.0.0.0/8,172.16.0.0/12`). Bindery refuses to start in proxy mode if this is empty. |
| `BINDERY_PROXY_AUTH_HEADER` | `X-Forwarded-User` | Header name Bindery reads for the authenticated username. |
| `BINDERY_PROXY_AUTO_PROVISION` | `true` | When `true`, a Bindery user is created on first login if none exists for that username. Set to `false` to require users to exist before they can log in. |

## Enabling proxy mode

1. Set `BINDERY_TRUSTED_PROXY` to your proxy's IP/CIDR.
2. Set auth mode to `proxy` via **Settings → General → Security → Authentication Mode**, or via the API:
   ```
   PUT /api/v1/auth/mode
   {"mode": "proxy"}
   ```
3. Confirm in startup logs: `trusted proxies: [<your CIDRs>]`.

The login page hides the password form and shows "Sign in via your SSO provider" when proxy mode is active.

## Traefik + Authelia

```yaml
# docker-compose.yml
services:
  authelia:
    image: authelia/authelia:latest
    # ... your Authelia config

  bindery:
    image: ghcr.io/vavallee/bindery:latest
    environment:
      BINDERY_TRUSTED_PROXY: "172.20.0.0/16"   # Docker network CIDR
      BINDERY_PROXY_AUTH_HEADER: "Remote-User"   # Authelia's default header
    labels:
      traefik.http.routers.bindery.middlewares: authelia@docker
      traefik.http.middlewares.authelia.forwardauth.address: http://authelia:9091/api/verify?rd=https://auth.example.com/
      traefik.http.middlewares.authelia.forwardauth.trustForwardHeader: "true"
      traefik.http.middlewares.authelia.forwardauth.authResponseHeaders: "Remote-User,Remote-Groups,Remote-Name,Remote-Email"
```

Authelia sets `Remote-User` (not `X-Forwarded-User`) by default. Either set `BINDERY_PROXY_AUTH_HEADER=Remote-User` or configure Authelia to use a different header name.

## Caddy + Authentik

```caddyfile
bindery.example.com {
    forward_auth authentik:9000 {
        uri /outpost.goauthentik.io/auth/caddy
        copy_headers X-Authentik-Username X-Authentik-Groups X-Authentik-Email
    }
    reverse_proxy bindery:8787
}
```

```yaml
# bindery env
BINDERY_TRUSTED_PROXY: "172.20.0.0/16"
BINDERY_PROXY_AUTH_HEADER: "X-Authentik-Username"
```

## nginx + Authelia (`auth_request`)

```nginx
# nginx.conf
server {
    listen 443 ssl http2;
    server_name bindery.example.com;

    ssl_certificate     /etc/letsencrypt/live/bindery.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/bindery.example.com/privkey.pem;

    # Internal endpoint nginx uses to verify the session with Authelia
    location = /authelia {
        internal;
        proxy_pass http://authelia:9091/api/verify;
        proxy_set_header X-Original-URL $scheme://$http_host$request_uri;
        proxy_set_header Content-Length "";
        proxy_pass_request_body off;
    }

    location / {
        auth_request /authelia;
        # Forward the authenticated username header Authelia sets on success
        auth_request_set $user $upstream_http_remote_user;
        proxy_set_header Remote-User $user;

        proxy_pass http://bindery:8787;
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

```yaml
# bindery env
BINDERY_TRUSTED_PROXY: "10.0.0.0/8"        # nginx container/server IP range
BINDERY_PROXY_AUTH_HEADER: "Remote-User"    # header set by auth_request_set above
```

## Kubernetes (Helm)

```yaml
# values.yaml
env:
  BINDERY_TRUSTED_PROXY: "10.0.0.0/8"
  BINDERY_PROXY_AUTH_HEADER: "X-Forwarded-User"
  BINDERY_PROXY_AUTO_PROVISION: "true"
```

For Traefik Ingress with Authelia forward auth middleware, annotate the Bindery Ingress:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: bindery
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: default-authelia@kubernetescrd
```

## Header choice: stability matters

Auto-provisioning ties a Bindery user row to the username in the header. If your IdP can change a user's username (e.g. email rename in Authentik), a new Bindery user gets created and the old user's data becomes inaccessible.

Prefer a **stable, opaque** identifier over a mutable display name or email address. Many proxies set `X-Forwarded-Preferred-Username` from an OIDC `preferred_username` claim — this is still a display name and can change. Use a UUID or internal user ID instead.

| IdP | Recommended approach |
|-----|---------------------|
| Authelia | `Remote-User` maps to the Authelia username. Stable if you never rename accounts; if you do, configure a custom header mapping to the internal UUID. |
| Authentik | Configure a property mapping that exposes the user's UUID in a custom header (e.g. `X-Authentik-UID`), then set `BINDERY_PROXY_AUTH_HEADER=X-Authentik-UID`. The default `X-Authentik-Username` changes on rename. |
| Keycloak | Add a custom mapper in Keycloak that passes the `sub` claim (a stable UUID) in a request header, then point `BINDERY_PROXY_AUTH_HEADER` at it. |
| nginx auth_request | Use `auth_request_set` to forward whichever stable header your IdP provides (see nginx example above). |

Avoid using email as the identity header — email addresses change and are not guaranteed unique across IdPs.

If a rename happens and an orphaned user is created, an admin can merge users from **Settings → Users**.

## Rollback

Proxy mode is a binary change — no schema migration is involved. To revert:

```
PUT /api/v1/auth/mode
{"mode": "enabled"}
```

Remove or unset `BINDERY_TRUSTED_PROXY`. Restart Bindery. Users keep their accounts; they will need to log in with a password.

## See also

- [docs/troubleshooting-auth.md](troubleshooting-auth.md) — consolidated symptom→cause→fix table for all auth phases

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Bindery refuses to start: `proxy mode requires BINDERY_TRUSTED_PROXY` | `BINDERY_TRUSTED_PROXY` is empty | Set `BINDERY_TRUSTED_PROXY` to your proxy's IP or CIDR. Proxy mode will not start without it — this is intentional to prevent auth bypass. |
| Every request returns `401 Unauthorized` | Source IP not in `BINDERY_TRUSTED_PROXY` | Check startup log for `trusted proxies: [...]`. The request source IP must match. In Docker, use the bridge network CIDR, not the container IP. |
| Every request returns `401 Unauthorized` | Header name mismatch | Authelia uses `Remote-User`; Authentik uses `X-Authentik-Username`. Set `BINDERY_PROXY_AUTH_HEADER` to match your proxy's output. Inspect request headers at the Bindery container with `BINDERY_LOG_LEVEL=debug`. |
| Login page still shows password form | Auth mode not set to `proxy` | `GET /api/v1/auth/status` — confirm `"mode": "proxy"`. If not, set it via Settings or the API. |
| New user created on every IdP username change | Mutable identifier in header | Switch to a stable IdP identifier (see "Header choice" above). Merge orphaned users from Settings → Users. |
| `X-Forwarded-User: admin` accepted from an untrusted LAN host | `BINDERY_TRUSTED_PROXY` too broad (e.g. `0.0.0.0/0`) | Tighten the CIDR to only your proxy's IP or pod subnet. Verify with `kubectl logs` or `docker logs` that the trusted CIDR list is correct. |
| OIDC logout from IdP doesn't log out of Bindery | Session cookie is HMAC-signed; no per-session revocation | Session expires at cookie TTL. To force logout: regenerate the session secret in Settings → General → Security (invalidates all sessions). A revocation list is planned for a future release. |
