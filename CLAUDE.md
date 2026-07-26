# Bindery

Go service, deployed to k8s via Helm + ArgoCD. Repo: vavallee/bindery. Docker: `ghcr.io/vavallee/bindery:{version}`, `:latest`, `:sha-{short}`.

## Release flow

All PRs target `main`. Tag push triggers CI:

1. `test` (no -race) gates → `image` + `goreleaser` run in parallel. `race` runs parallel, non-blocking.
2. `image`: Docker push (linux/amd64), then bumps `charts/bindery/values-dev.yaml` on `development` → `bindery-dev` ArgoCD app refreshes. Dev-first so changes are validated before prod.
3. `goreleaser`: binaries for linux (amd64/arm64/armv6/armv7), darwin, windows + SBOMs → GitHub Release.

**CHANGELOG.md entry for the version MUST exist before tagging** — the release step reads it and aborts if missing. Tag the commit that contains the entry. (Learned the hard way on v1.1.1.)

**A `v*` tag deploys automatically** — there is no manual promotion step. `deploy-prod` is gated on the tag plus a green `goreleaser`, bumps `charts/bindery/values.yaml` on main, and the ArgoCD `bindery` app syncs it. `notify-discord` posts the release announcement at the same time. (The old "Promote to production" workflow_dispatch flow is gone; this file described it long after it was removed.)

The ArgoCD "prod" app is the maintainer's own instance, not a customer fleet — the parts that actually reach other people are the public GitHub Release and the Discord post.

## Security conventions (from the v1.1.1 multi-user audit)

- Any new auth/settings endpoint: check for `RequireAdmin` and that responses don't leak sensitive settings to non-admins.
- User-scoped resources must filter by `owner_user_id` — no cross-user visibility.
- Never trust `X-Forwarded-*` — `trustedProxyMiddleware` strips them; keep new handlers behind it.

## Deployment notes

- `BINDERY_PUID/PGID` are sanity checks only (distroless image, no runtime user switching) — operators must also set `user: "UID:GID"` in Compose.
