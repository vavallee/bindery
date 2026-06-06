# Contributing to Bindery

Thanks for your interest. Bindery is a single-maintainer project that takes PRs, issues, and feedback via [GitHub](https://github.com/vavallee/bindery/issues). The goal is a small, reliable, audited codebase — PRs that narrow the diff and tighten guarantees are preferred over PRs that add surface area.

For quick questions about dev setup, build failures, or whether an idea fits scope, the Discord server is usually faster than opening an issue: [discord.gg/RpuYYRM9cZ](https://discord.gg/RpuYYRM9cZ). **Do not** post security vulnerabilities there — see [Reporting security issues](#reporting-security-issues) below.

## Prerequisites

- Go 1.25+
- Node.js 22+
- Docker (for container build verification)

## Build

```bash
# Backend only
go build ./cmd/bindery

# Frontend
cd web && npm ci && npm run build

# Full image (multi-arch rehearsal)
docker buildx build --platform linux/amd64,linux/arm64 -t bindery:local .

# Release cross-compile rehearsal (no publish, no tag)
goreleaser release --snapshot --clean
```

## Project layout

```
bindery/
├── cmd/bindery/           # Application entry point
├── internal/
│   ├── api/               # HTTP handlers (chi router)
│   ├── auth/              # Argon2id passwords, HMAC sessions
│   ├── db/                # SQLite repository layer + migrations
│   ├── models/            # Domain types
│   ├── metadata/          # OpenLibrary, Google Books, Hardcover, Audnex
│   ├── indexer/           # Newznab/Torznab client + multi-indexer searcher
│   ├── downloader/        # SABnzbd + qBittorrent clients
│   ├── importer/          # Filename parser, renamer, scanner
│   ├── notifier/          # Webhook dispatcher
│   ├── scheduler/         # Background job runner (cron)
│   ├── webui/             # go:embed for React dist
│   └── config/            # Environment-based configuration
├── web/                   # React frontend (Vite)
├── charts/bindery/        # Helm chart
├── docs/                  # Deployment, roadmap, ABS import guide
└── .github/workflows/     # CI/CD
```

## Quality & security checks

Every push to `main` / `development` and every pull request runs through the full check matrix in [`.github/workflows/ci.yml`](.github/workflows/ci.yml). A release tag (`v*`) cannot be cut unless all of the below pass.

### Backend (Go)

| Check | Tool | Scope |
|-------|------|-------|
| Formatting | `gofmt` (enforced via `golangci-lint`) | All `.go` files — no unformatted code reaches `main` |
| Static analysis / lint | `golangci-lint v2.11.4` (`--timeout=5m`) | Full repo. Enabled linters (see `.golangci.yml`): `govet`, `staticcheck`, `errcheck`, `unused`, `ineffassign`, `revive`, `gosec`, `errorlint`, `noctx`, `misspell`, plus the resource-leak detectors `bodyclose`, `sqlclosecheck`, `rowserrcheck` |
| Vet | `go vet` (via `golangci-lint`) | Full repo |
| Vulnerability scan | `govulncheck ./...` | Resolves every imported symbol against the Go vulnerability database; fails on known CVEs in the transitive module graph |
| Unit / integration tests | `go test ./cmd/... ./internal/...` | All packages; test DBs use in-memory SQLite (`db.OpenMemory`) so no disk state leaks between runs |

### Frontend (React + TypeScript)

| Check | Tool | Scope |
|-------|------|-------|
| Type-check | `tsc --noEmit` (`npm run typecheck`) | Strict mode; full `web/src/**` |
| Lint | ESLint 9 flat config (`npm run lint`) | `@eslint/js` + `typescript-eslint` + `eslint-plugin-react-hooks` + `eslint-plugin-react-refresh` |
| Build | `tsc -b && vite build` (`npm run build`) | Emits the embedded SPA. Build failures (import errors, TS errors, missing assets) block the pipeline |
| Unit tests | `vitest run --passWithNoTests` | Runs when tests exist (`@testing-library/react` + `jsdom` configured) |

### Container & dependency supply chain

| Check | Tool | Scope |
|-------|------|-------|
| Image build | `docker/build-push-action@v6` with `linux/amd64` + `linux/arm64` | Multi-stage build using distroless `nonroot` base — no shell, no package manager, no root user |
| Base image pinning | Dockerfile pins distroless digest | Protects against upstream tag-mutation |
| Provenance / attestations | GitHub Actions OIDC + `packages: write` + `security-events: write` permissions | Published container has attached SLSA provenance |
| Helm chart | `charts/bindery/` rendered and image tag auto-bumped per merge | Image digest lands in `values.yaml` via CI commit (`[skip ci]`) |
| Dependency pinning | `go.sum` (Go), `package-lock.json` (npm), `charts/bindery/Chart.yaml` (Helm) | All committed; `npm ci` refuses to install if the lockfile drifts |

### Credentials & secrets

Bindery is deliberately **credential-free in source**. The CI pipeline enforces this:

- **Secret scanning** — GitHub's native Push Protection + Secret Scanning is enabled on `vavallee/bindery`. Commits containing a detected token (AWS keys, Google API keys, GitHub PATs, etc.) are blocked at push time.
- **No runtime secret required to ship** — the image runs without any env var. `BINDERY_API_KEY` is a one-time seed; the real key is generated on first boot and stored in SQLite. The session-signing HMAC secret is likewise generated at bootstrap and never travels via env.
- **Passwords** — hashed with argon2id (OWASP 2024 parameters). Nothing reversible is stored.
- **Session cookies** — `HttpOnly` + `SameSite=Lax`, signed HMAC-SHA256. Not JWT, not server-side sessions — self-contained and invalidated by rotating the signing secret.
- **Per-IP login rate limit** — 5 failures / 15 minutes → `429`. Blocks credential-stuffing on publicly exposed deployments.
- **Downstream integrations (SABnzbd, qBittorrent, indexer API keys, Google Books, Hardcover)** — stored only in the SQLite DB after you enter them in the UI, never committed, never logged in plain text. `GET /setting` filters `auth.*` keys so they cannot be exfiltrated via the generic settings API.

### Release gating

Tag pushes (`v*`) run every check above **before** the GitHub Release is cut:

1. Go tests + golangci-lint + govulncheck (blocking)
2. Frontend typecheck + lint + build (blocking)
3. Docker multi-arch build (blocking)
4. GoReleaser cross-compiles `linux/{amd64,arm64,armv7,armv6}`, `darwin/{amd64,arm64}`, `windows/{amd64,arm64}` and attaches SHA-256 checksums to the release (`bindery_vX.Y.Z_checksums.txt`)

A failing check at any step aborts the release — no artifacts are published from a red pipeline.

### Running the full local check suite

The fastest path is the one-shot target, which runs the same things the gating
CI checks run:

```bash
make check
```

That is equivalent to the commands below; run them individually while iterating:

```bash
# Go: format, vet, lint, vuln scan, test
gofmt -l . | tee /dev/stderr | (! read)        # fails if any file is unformatted
go vet ./...
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4
golangci-lint run --timeout=5m
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
go test ./cmd/... ./internal/...

# Frontend: lockfile-strict install, lint, typecheck, build, tests
cd web
npm ci
npm run lint
npm run typecheck
npm run build
npm test
cd ..

# Docker: cross-arch build smoke test
docker buildx build --platform linux/amd64,linux/arm64 -t bindery:local .

# Release rehearsal (no publish, no tag)
goreleaser release --snapshot --clean
```

### Which checks actually block a PR

Bindery runs a deliberately heavy security suite (CodeQL, Semgrep, gosec, Grype,
Checkov, Hadolint, gitleaks, ZAP DAST, SBOM, Scorecard). **Most of those are
advisory** — they surface findings in the Security tab but do not block merge,
and some go red for reasons unrelated to your change (e.g. *Container Scan*
flagging a CVE in the base image). Only **lint**, **validate (Go)**, and
**Security Summary** are required to merge. If a non-required scan is red,
mention it in the PR and a maintainer will tell you whether it's pre-existing.

New/changed lines should be ≥70% covered (enforced on the patch via Codecov);
doc- and config-only PRs have no measured lines and pass automatically.

## Database migrations — numbering convention

Schema changes are additive SQL files in `internal/db/migrations/`, applied
idempotently at startup, named `NNN_short_description.sql`.

- **Use the next free number** — look at the highest existing file and add one.
- **The gap at `010` is intentional**; numbering follows the filename prefix, not
  slice position. Don't fill the gap.
- Two files sharing a number are rejected at boot (the duplicate-version guard),
  so if your branch and `main` both added `0NN`, **renumber yours** to the next
  free slot when you rebase.
- Migrations are forward-only and must not destructively rewrite existing rows.

## Changelog — add a fragment, don't edit CHANGELOG.md

Editing the single `CHANGELOG.md` in every PR causes constant merge conflicts.
Instead, drop a small fragment under [`changelog.d/`](changelog.d/) and leave
`CHANGELOG.md` alone — a maintainer assembles fragments at release time. One
fragment per PR; format and examples are in
[`changelog.d/README.md`](changelog.d/README.md). Preview with `make changelog`.

## Pull request flow

1. Fork the repo.
2. Create a feature branch (`git checkout -b feature/x` or `fix/NN-description` for issue links).
3. Make the change. Keep the diff narrow — bug fixes don't need surrounding cleanup; one-shot operations don't need helpers.
4. Add or adjust tests. Backend: follow the `internal/api/*_test.go` pattern (in-memory SQLite via `db.OpenMemory`, `httptest` handlers). Frontend: `vitest` + `@testing-library/react` with `jsdom`.
5. Run the full local check suite above — every item must pass.
6. Open a PR. Tie it to the tracking issue with `Closes #NN` in the body when applicable.

## Commit messages

Follows [Conventional Commits](https://www.conventionalcommits.org/) loosely. Recent examples from the repo:

- `feat(release): cross-platform binaries via GoReleaser`
- `feat: metadata language filter (#14)`
- `fix: author delete can sweep files (#15)`
- `chore(deploy): update bindery image to sha-xxxxxxx [skip ci]`
- `docs: add roadmap (multi-user, SSO, external DB)`

The `[skip ci]` trailer is reserved for bot deploy-commits — human commits should always go through CI.

## Reporting security issues

Do **not** open a public issue for security vulnerabilities. The preferred channel is a [GitHub Security Advisory](https://github.com/vavallee/bindery/security/advisories/new) — it creates a private thread with the maintainers. Full disclosure policy, scope, and timelines live in [SECURITY.md](SECURITY.md).
