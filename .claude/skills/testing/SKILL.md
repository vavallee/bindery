---
name: testing
description: Use when writing or running Bindery tests, before pushing, or before opening a PR — covers the full pre-PR matrix (lint, vet, govulncheck, test, frontend), pinned tool versions, the project's test patterns (db.OpenMemory, httptest, vitest), and common failure modes.
---

# Testing Bindery

CI runs every check below on every push and PR. Mirror it locally before pushing — failing CI is slower than failing locally.

## Pre-PR matrix

```bash
# Go
gofmt -l . | tee /dev/stderr | (! read)    # exits non-zero if any file is unformatted
go vet ./...
golangci-lint run --timeout=5m             # pinned to v2.11.4 — see below
govulncheck ./...
go test -race -coverprofile=coverage.out -covermode=atomic ./cmd/... ./internal/...

# Frontend (skip if web/ untouched)
cd web && npm ci && npm run lint && npm run typecheck && npm run build && npm test
```

Make wrappers for convenience:
- `make lint` — Go + frontend linters
- `make test` — Go race + coverage
- `make test-web` — `vitest` + coverage

For wiring-level changes (handlers, scheduler jobs, downloader integrations, import flows), unit tests aren't enough. Escalate to `make smoke` — see the `smoke-testing` skill for the decision tree.

## Pinned tool versions

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4
go install golang.org/x/vuln/cmd/govulncheck@latest
```

CI uses `golangci-lint v2.11.4` exactly. `@latest` locally can disagree with CI and waste a round-trip.

## Test patterns

**Backend (Go)**

- One `_test.go` file per source file, sibling location (`internal/api/books.go` → `internal/api/books_test.go`).
- DB-backed tests use `db.OpenMemory()` from `internal/db`. Never use temp files; never mock the DB. Mocked DB tests pass when migrations are broken.
- HTTP handlers: `httptest.NewRecorder` + `httptest.NewRequest`, exercise routes through the same setup as production.
- Assertions on wrapped errors: `errors.Is` / `errors.As` (errorlint rejects `==`).
- Always check `rows.Err()` after iterating SQL rows (rowserrcheck enforces it).
- Outbound HTTP in tests: stub via `httptest.NewServer`, never live network.

**Frontend (React + TS)**

- Pages: `web/src/pages/<Name>Page.tsx` with sibling `<Name>Page.test.tsx`.
- `vitest` + `@testing-library/react` + `jsdom`; setup in `web/src/test-setup.ts`.
- Mock the API client (`web/src/api/`) — never hit the network.
- Prefer `userEvent` over `fireEvent` for interactions.
- Run a single page test fast: `npm test -- BooksPage.test.tsx` (no watch).

## When new code requires new tests

| Change | Tests required? |
|--------|-----------------|
| New HTTP handler in `internal/api/` | Yes — sibling `_test.go` covering the success path and at least one error path |
| New exported function with branching | Yes |
| New domain logic in `internal/{indexer,downloader,importer,...}` | Yes |
| New React component with state, effects, or event handlers | Yes |
| Pure rendering / styled markup | Optional but appreciated |
| Pure refactor, no behaviour change | Existing tests must still pass; new tests not required |

## Common failure modes

| Symptom | Likely cause |
|---------|--------------|
| `gofmt` flags a file you didn't touch | Merge-base drift; run `gofmt -w <path>` |
| `govulncheck` fails with a CVE you didn't introduce | New advisory in a transitive dep. Bump the offender; call out the bump in the PR body |
| Every test in a package fails | A migration regression. Each package opens a fresh in-memory DB and runs all migrations — one broken migration takes the whole package down |
| `vitest` passes, `vite build` fails | Missing static asset import or type error `tsc --noEmit` didn't catch. Run typecheck + build together |
| Test passes locally, fails on CI with `-race` | A goroutine leak or unsynchronized map access. Run locally with `-race -count=10` to reproduce |
| Smoke suite hangs at startup | Binary panicking before binding. Run `./bindery` directly, read stderr |

## Don't

- Don't `t.Skip()` a flaky test to make CI green — investigate or revert the change that introduced flake.
- Don't mock `internal/db`. Use the in-memory DB.
- Don't add new build tags (`//go:build foo`) — the project doesn't use them.
- Don't add `//nolint` without a reason on the same line; readers and CI both expect a justification.
- Don't commit `coverage.out` or `web/coverage/` — they're build artifacts.
