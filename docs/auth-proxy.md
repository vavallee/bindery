# Reverse-Proxy SSO Authentication

Bindery supports a `proxy` auth mode, added in v0.23.0, that delegates identity to an upstream reverse proxy — Authelia, Authentik, Keycloak, Google, GitHub, or any system that sets a trusted identity header.

> **Security warning:** Proxy mode is only safe if your Bindery instance is not directly reachable from untrusted networks. Any client that can reach Bindery and forge `X-Forwarded-User` without going through your proxy can authenticate as any user. Use firewall rules or network policy to enforce this. The proxy itself must also remove or overwrite the identity header on every route to Bindery, or a visitor can forge it through the proxy; see [Your proxy must own the identity header](#your-proxy-must-own-the-identity-header).

## How it works

When `mode=proxy`, Bindery reads an identity header (default `X-Forwarded-User`) on every request. If the request arrives from a trusted proxy IP and the header is present, Bindery resolves or auto-provisions a user by that username and issues a session.

"From a trusted proxy IP" means the address of the TCP connection itself, the proxy that connected to Bindery. It is never the visitor address in `X-Forwarded-For`. A proxy that appends `X-Forwarded-For` (Cloudflare Tunnel, Traefik, nginx, Caddy, almost all of them) is recognised by its own address, and `X-Forwarded-For` from a host that is not in `BINDERY_TRUSTED_PROXY` is stripped, so a host that is not your proxy cannot claim to be one by forging it. Bindery 1.40.2 and earlier checked the forwarded visitor address instead and rejected every proxied login ([#3096](https://github.com/vavallee/bindery/issues/3096)).

### Your proxy must own the identity header

Bindery believes the identity header on any request that arrives from a trusted proxy, whoever put it there. If the proxy passes a header the visitor sent straight through, the visitor can sign in as any user, including the admin. So the proxy has to **remove or overwrite the identity header on every route that reaches Bindery**, not just on the routes behind your SSO check. A route, path or hostname that forwards to Bindery without the auth step is a way in.

- **nginx:** set the header explicitly in every `location` that proxies to Bindery, for example `proxy_set_header Remote-User $user;` as in the example below. `proxy_set_header` replaces whatever the client sent, and an empty value drops the header. A `location` that has any `proxy_set_header` of its own does not inherit the ones from the `server` block, so repeat it in each one.
- **Traefik:** `forwardauth.authResponseHeaders` replaces the listed headers with the auth server's values, but only on routers that use that middleware. Put the forward auth middleware on every router to Bindery, and if any router to Bindery must stay open, give it a `headers` middleware that removes the header, for example `traefik.http.middlewares.strip-user.headers.customRequestHeaders.Remote-User: ""` (an empty value removes the header).
- **Caddy:** with `forward_auth` and `copy_headers`, as in the example below, recent Caddy already drops any client copy of the listed headers, so Bindery only ever sees the auth service's value. No extra stripping is needed; keep Caddy up to date and keep every path to Bindery behind `forward_auth`. Do not add a bare `request_header -<header>` to the site block: in Caddy's default directive order it runs after `forward_auth` and would delete the real header, locking everyone out. Only if the header reaches Bindery some other way, strip the client copy inside a `route { request_header -X-Authentik-Username ... }` block (your header name), where directives run in the order written.
- **Cloudflare Access with Cloudflare Tunnel:** Access sets `Cf-Access-Authenticated-User-Email` only on requests to an application it protects. Every hostname and every path that reaches Bindery must be covered by an Access application whose policies are Allow rules, with **no Bypass policy**. A Bypass rule (for example one added so an ebook reader can reach `/opds` without logging in) or a hostname or path the tunnel routes to Bindery that no application covers lets a forged header through `cloudflared`, which connects from your trusted CIDR. With `BINDERY_URL_BASE` set, make sure the application covers that path and anything else on the hostname that the tunnel sends to Bindery.

Cloudflare recommends that the origin also validate the signed `Cf-Access-Jwt-Assertion` token rather than trust the email header alone. **Bindery does not validate it today**, so the only protection is the configuration above plus making sure nothing but `cloudflared` can reach Bindery.

**Bindery refuses to start in proxy mode if `BINDERY_TRUSTED_PROXY` is empty** — this is intentional. The startup log emits the trusted CIDR list so you can verify it in `kubectl logs` or `docker logs`.

> **Running `local-only` behind a proxy instead?** That mode has no equivalent startup gate, because a direct-to-LAN install is a valid deployment, but it needs `BINDERY_TRUSTED_PROXY` for the same reason: without it Bindery resolves the client IP as the proxy's own private address and serves every proxied request as a trusted local client. See [DEPLOYMENT.md](DEPLOYMENT.md#first-run-setup).

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
2. Set auth mode to `proxy` with the API. The Authentication Mode dropdown in **Settings → General → Security** offers only `enabled`, `local-only` and `disabled`, so proxy mode has to be set this way:
   ```
   PUT /api/v1/auth/mode
   {"mode": "proxy"}
   ```
3. Confirm in startup logs: the line `proxy auth mode: trusted proxies`, whose `cidrs` field lists your CIDRs.

The refuse-to-start gate runs at boot only. Switching to proxy mode through the API on a process that started without `BINDERY_TRUSTED_PROXY` is accepted, every request then answers 401, and the next restart is the one that refuses. Set the variable and restart before you flip the mode.

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

## Roles

Proxy mode carries identity only. There is no header that sets a role: Bindery does not read a groups or roles header from the proxy, and an auto-provisioned user is always created as `user`. To make someone an `admin` or a [`requester`](multi-user.md#requester), let them sign in once so the account exists, then change the role on the **Users** page or with `PUT /api/v1/auth/users/{id}/role`. The role is read from the database on every request, so the change applies on their next page load.

Mapping a proxy header to a role is deliberately not built: a header is only as trustworthy as the network path to Bindery, and a forged role header would be a forged admin.

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

If a rename happens and an orphaned user is created, there is no merge. Delete the orphan from the **Users** page and hand its library rows to the new account: `DELETE /api/v1/auth/users/<orphan-id>?strategy=reassign&reassignTo=<new-id>`. Deleting without a strategy a user who owns rows answers 409 with the per table counts, so you can see what is at stake first.

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
| Bindery refuses to start: `proxy auth mode is active but BINDERY_TRUSTED_PROXY is empty` | `BINDERY_TRUSTED_PROXY` is empty | Set `BINDERY_TRUSTED_PROXY` to your proxy's IP or CIDR. Proxy mode will not start without it — this is intentional to prevent auth bypass. |
| Every request returns `401 Unauthorized` | Source IP not in `BINDERY_TRUSTED_PROXY` | Check the startup log for `proxy auth mode: trusted proxies` and read its `cidrs` field. The request source IP must match. In Docker, use the bridge network CIDR, not the container IP. |
| Log shows `proxy auth: identity header from untrusted source, rejecting` | The connecting address is not in `BINDERY_TRUSTED_PROXY` | The line carries `peer` (the address that connected to Bindery, the one the decision is made on) and `client` (the visitor resolved from `X-Forwarded-For`). Add `peer` to `BINDERY_TRUSTED_PROXY`, never `client`. With Cloudflare Tunnel that is the `cloudflared` container, for example the Docker bridge `172.17.0.0/16`. On 1.40.2 and earlier `peer` held the visitor address and every proxied request was rejected; upgrade rather than widening the CIDR (#3096). |
| Every request returns `401 Unauthorized` | Header name mismatch | Authelia uses `Remote-User`; Authentik uses `X-Authentik-Username`. Set `BINDERY_PROXY_AUTH_HEADER` to match your proxy's output. Inspect request headers at the Bindery container with `BINDERY_LOG_LEVEL=debug`. |
| Login page still shows password form | Auth mode not set to `proxy` | `GET /api/v1/auth/status` — confirm `"mode": "proxy"`. If not, set it with `PUT /api/v1/auth/mode`; the Settings dropdown does not offer proxy mode. |
| New user created on every IdP username change | Mutable identifier in header | Switch to a stable IdP identifier (see "Header choice" above). There is no merge: reassign the orphan's data to the new account when deleting it from the **Users** page. |
| `X-Forwarded-User: admin` accepted from an untrusted LAN host | `BINDERY_TRUSTED_PROXY` too broad (e.g. `0.0.0.0/0`) | Tighten the CIDR to only your proxy's IP or pod subnet. Verify with `kubectl logs` or `docker logs` that the trusted CIDR list is correct. |
| OIDC logout from IdP doesn't log out of Bindery | Session cookie is HMAC-signed and independent of the IdP session | Session expires at cookie TTL. To force a global logout, rotate the session secret in Settings, General, Security twice: one rotation keeps the previous secret valid so nobody is dropped. To evict one account, reset that user's password, which bumps their session epoch. Logging out of Bindery revokes that one session on the server. |
