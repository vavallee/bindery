# Bindery

Go service, deployed to k8s via Helm + ArgoCD. Repo: vavallee/bindery. Docker: `ghcr.io/vavallee/bindery:{version}`, `:latest`, `:sha-{short}`.

## Release flow

All PRs target `main`. Tag push triggers CI:

1. `test` (no -race) gates → `image` + `goreleaser` run in parallel. `race` and `lint` run parallel, non-blocking.
2. `image`: Docker push (linux/amd64 + linux/arm64), including `:latest`. It needs only `test`, so a tag whose `goreleaser` or smoke fails has still moved `:latest`. A tag does not touch dev: `deploy-dev` runs only for the `development` branch, where it sets `image.tag` to `sha-{short}` in `charts/bindery/values-dev.yaml` and refreshes the `bindery-dev` ArgoCD app.
3. `goreleaser`: binaries for linux (amd64/arm64/armv6/armv7), darwin, windows + SBOMs → GitHub Release.

**CHANGELOG.md entry for the version MUST exist before tagging** — the release step reads it and aborts if missing. Tag the commit that contains the entry. (Learned the hard way on v1.1.1.)

**A `v*` tag deploys automatically.** There is no manual promotion step. `deploy-prod` needs `image`, `goreleaser` and `predeploy-smoke` on a tag, sets `image.tag` in `charts/bindery/values.yaml` on main to the release version (a tag, not a digest), and the ArgoCD `bindery` app syncs it. `notify-discord` needs `image` and `goreleaser` and posts the release announcement without waiting for the deploy. (The old "Promote to production" workflow_dispatch flow is gone; this file described it long after it was removed.)

**The chart on main runs ahead of the prod image.** The prod ArgoCD app syncs `charts/bindery` from main, while `image.tag` in `values.yaml` moves only at a release. A chart change that needs a newer binary must wait until prod runs that image (merging one early caused a 10 hour outage on 2026-11-14, #3885).

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

## Local resource limits (agents on DARKWAVE2)

The dev host is a WSL2 VM capped at 12 GB RAM + 8 GB swap, shared with other sessions. On 2026-10-08 two concurrent `golangci-lint` runs from parallel subagent worktrees (5.5 GB + 3.8 GB) filled swap to 7.6 of 8 GB. When several agents or worktrees are active:

- Run heavy jobs one at a time through one shared lock: `flock /tmp/bindery-heavy.lock <cmd>`. Heavy means `golangci-lint`, `go test ./internal/api/...` (or `make test`), `npm ci`, `tsc`, `make build` and web builds.
- Set `GOMEMLIMIT=3GiB` for Go tools.
- Lint with `golangci-lint run --concurrency 2 ./...`. Never pass `--allow-parallel-runners`; its own lock is what keeps two runs from overlapping.
- Run Go tests with `-p 2`.
- Remove finished worktrees (`git worktree remove <path>`) when their PR lands.
