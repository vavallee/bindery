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

## Licensing conventions (from the 2026-08 legal sweep)

**Bindery is MIT. The *arr projects this codebase takes architectural cues from are GPL-3.0.** That difference is the whole reason this section exists: their dependency habits are safe for them and unsafe for us. A GPL project linking a GPL library is the intended use; an MIT project doing the same makes every distributed binary a combined work that cannot be offered under MIT.

- **Check the licence of every new Go module and npm package before adding it.** Read the actual `LICENSE` in the module cache — do not infer from the ecosystem or from a sibling library.
- **Copyleft (GPL / AGPL / SSPL / BUSL / CC-BY-NC) is disqualifying** for anything linked into the binary or bundled into the web build. Go static linking means there is no "we only use a bit of it".
- **Fuzzy-matching and similarity libraries are a known trap.** Both Go FuzzyWuzzy ports are GPL-3.0, inherited from the copyleft Python original — `creditx/go-fuzzywuzzy` shipped in MIT binaries for months before anyone checked (#1988). Permissive alternatives: `adrg/strutil` (MIT), `hbollon/go-edlib` (MIT), `agext/levenshtein` (Apache-2.0).
- **Attribution is not optional.** Apache-2.0 §4(d) requires shipping the dependency's `NOTICE`; MIT and BSD-3-Clause require retaining copyright notices in binary redistribution. `THIRD_PARTY_LICENSES.md` is generated and CI fails on drift (#1989) — regenerate it rather than hand-editing.
- **Naming other projects is fine; borrowing their identity is not.** Nominative use ("the Readarr replacement", an integrations table) is legitimate and the README carries a disclaimer. Visual identity is a separate question.

## Deployment notes

- `BINDERY_PUID/PGID` are sanity checks only (distroless image, no runtime user switching) — operators must also set `user: "UID:GID"` in Compose.
