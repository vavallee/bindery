# AGENTS.md

Operational guide for AI coding agents (Claude Code, Codex, Cursor, etc.) working in this repo. Humans should read [CONTRIBUTING.md](CONTRIBUTING.md) first — this file is a condensed, task-oriented overlay that assumes you already have it.

## What Bindery is

A single-binary book download manager (Readarr replacement) — Go 1.25 backend with an embedded React 19 SPA, SQLite via `modernc.org/sqlite` (no CGO), distroless container image. Architecture, feature surface, and deployment are documented in [README.md](README.md) and [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

## Repo map

```
cmd/bindery/         entry point, migrate / proxy / reconcile / healthcheck subcommands
internal/api/        chi HTTP handlers — one file per resource, *_test.go alongside
internal/auth/       argon2id passwords, HMAC sessions, OIDC, proxy auth
internal/db/         sqlite repository layer + migrations (use db.OpenMemory in tests)
internal/{indexer,downloader,importer,metadata,recommender,scheduler,notifier}/
                     business logic by domain
internal/webui/      go:embed wrapper for web/dist
web/                 React 19 + TS + Tailwind, Vite, vitest
charts/bindery/      Helm chart (image tag auto-bumped by CI)
docs/                deployment, ABS import, hardcover, calibre, roadmap
tests/{smoke,predeploy,abscontract,security}/
                     out-of-process suites
```

## Commands you will actually run

Use `make help` to discover targets. The ones agents need most:

| Task | Command |
|------|---------|
| Build binary (embeds web) | `make build` |
| Run backend in dev | `make dev` |
| Run frontend dev server | `make web-dev` (proxy to backend on `:8787`) |
| Backend unit + integration tests | `make test` (race + coverage) |
| Frontend tests | `make test-web` |
| All linters | `make lint` |
| Go-only / web-only lint | `make lint-go` / `make lint-web` |
| HTTP smoke suite (real binary) | `make smoke` |
| ABS contract suite (slow) | `make abs-contract` |
| Local security scanners | `make security` |
| Helm chart lint | `make helm-lint` |

Before reporting work complete, run the relevant subset — see the **`testing`** skill for the full pre-PR matrix and pinned tool versions, or [CONTRIBUTING.md §Running the full local check suite](CONTRIBUTING.md#running-the-full-local-check-suite) for the canonical reference.

## Conventions

**Go**

- Linter is `golangci-lint v2.11.4` with `gosec`, `revive`, `errorlint`, `bodyclose`, `noctx`, `rowserrcheck`, `sqlclosecheck`, `staticcheck` enabled. Read `.golangci.yml` before adding suppressions — many common warnings already have project-wide exclusions.
- HTTP handlers go in `internal/api/<resource>.go` with a sibling `<resource>_test.go`.
- DB access goes through `internal/db`; do not open `database/sql` connections elsewhere.
- Outbound HTTP must use the SSRF-guarded clients in `internal/httpsec`. Never `http.Get` raw user-provided URLs.
- Errors: wrap with `%w`, compare with `errors.Is/As`.

**Frontend**

- React 19 + TypeScript strict + Tailwind. ESLint 9 flat config in `web/eslint.config.js`.
- Pages live in `web/src/pages`, shared components in `web/src/components`, API client in `web/src/api`. i18n keys in `web/src/i18n` — every user-facing string flows through `useTranslation`.

Test patterns (`db.OpenMemory`, `httptest`, `vitest` + `@testing-library/react`) and the rule that tests are required for new handlers / domain logic / components-with-logic are documented in the **`testing`** skill.

## Things to be careful with

- **Migrations** are forward-only. Add a new file in `internal/db/migrations/` — never edit a migration that has shipped.
- **Schema drift in tests** — tests run real migrations against `db.OpenMemory`. If a migration fails, every test in the package fails; check there first.
- **`internal/webui/dist/`** is generated; `make build` copies `web/dist/*` into it before `go build`. Don't commit changes to it; `web/` is the source of truth.
- **CHANGELOG.md** is authored at release time only — see the **`tag-release`** skill. Don't add `[Unreleased]` entries during feature work; CI only validates that a `## [vX.Y.Z]` section exists at tag time.
- **Helm `values.yaml`** image digest is auto-bumped by CI with `[skip ci]`. Don't hand-edit the digest.
- **Secrets in source are blocked** by gitleaks + GitHub Push Protection. Use the SQLite-backed settings store (configured at runtime) for any credential — see `internal/auth` and `internal/config`.
- **The frontend talks to the backend over `/api/v1` only** (plus the `*arr-compatible `/api/queue` and `/opds/v1.2/`). Auth rules per route live in `internal/api/auth.go`.

## Working on a feature — lifecycle

1. **Issue first** for non-trivial work. Search existing issues; check `docs/ROADMAP.md`. The README explicitly asks contributors to open an issue before starting anything substantial.
2. **Branch** from `main`. Naming, scope vocabulary, and full message format are in the **`commits`** skill.
3. **Implement** — keep diffs narrow. CONTRIBUTING.md §"Pull request flow" is explicit: this project prefers tightening the diff over surrounding cleanup.
4. **Test** as you go. The **`testing`** skill covers patterns and the pre-PR matrix; **`smoke-testing`** covers when to escalate to out-of-process suites.
5. **Update docs** as part of the *commit*: `docs/` and `README.md` are maintained at every commit (full matrix in the **`commits`** skill). `CHANGELOG.md` is release-time only — leave it alone (see **`tag-release`**).
6. **Commit and open the PR** — see **`commits`** for the message format and branch naming, **`prs`** for the PR body skeleton, issue templates, and PR mechanics (draft → ready → squash-on-merge).

## Out of scope for agents

- **Releases.** Do not push tags (`v*`) — that triggers GoReleaser + provenance signing. The maintainer cuts releases. The **`tag-release`** skill drafts release artifacts (CHANGELOG section, version bump) for the maintainer to review before they tag.
- **`[skip ci]` commits.** Reserved for the deploy bot; human and agent commits must go through CI.
- **Security advisories.** If you find a vulnerability, surface it to the user — do not file a public issue. The disclosure flow is in [SECURITY.md](SECURITY.md).
- **Force-push, history rewrites, branch deletion** without explicit user instruction.
- **Adding scrapers or undocumented APIs** for metadata. The project is deliberately Goodreads-free; new sources must be documented public APIs (see README §"Metadata Sources").

## Project skills

The repo's `.claude/skills/` directory holds task-triggered skills for Claude-format-aware agents (Claude Code, Copilot CLI, Codex CLI, Gemini CLI). Skills are conditional workflows; this `AGENTS.md` is the always-on baseline.

| Skill | Fires when |
|-------|-----------|
| [`testing`](.claude/skills/testing/SKILL.md) | Writing or running tests, before pushing — pre-PR matrix, pinned versions, test patterns, common failure modes |
| [`smoke-testing`](.claude/skills/smoke-testing/SKILL.md) | Deciding whether to run an out-of-process suite — picks between `make smoke`, `make abs-contract`, `make predeploy-smoke` |
| [`commits`](.claude/skills/commits/SKILL.md) | Picking a branch name, staging, or writing a commit message — branches, Conventional Commits format, doc-update gate |
| [`prs`](.claude/skills/prs/SKILL.md) | Opening / updating a PR, responding to review, filing an issue — PR body skeleton, PR mechanics, issue templates |
| [`tag-release`](.claude/skills/tag-release/SKILL.md) | Cutting a release — composing the `## [vX.Y.Z]` CHANGELOG section and walking commits since the last tag (authoring only; maintainer pushes the tag) |

Non-Claude agents that can't auto-load `SKILL.md` files should read the bodies under `.claude/skills/<name>/SKILL.md` directly when a trigger condition fires.

## Reference

- Full CI matrix, security checks, and local rehearsal commands: [CONTRIBUTING.md](CONTRIBUTING.md)
- Deployment / env vars / upgrade path: [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)
- Roadmap and out-of-scope list: [docs/ROADMAP.md](docs/ROADMAP.md)
- Auth modes (multiuser / OIDC / proxy): [docs/auth-multiuser.md](docs/auth-multiuser.md), [docs/auth-oidc.md](docs/auth-oidc.md), [docs/auth-proxy.md](docs/auth-proxy.md)
- Vulnerability disclosure: [SECURITY.md](SECURITY.md)
