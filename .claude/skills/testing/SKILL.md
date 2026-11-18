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
go test -coverprofile=coverage.out -covermode=atomic ./cmd/... ./internal/... ./tools/... ./tests/integration/... ./tests/security/...

# Frontend (skip if web/ untouched)
cd web && npm ci && npm run lint && npm run typecheck && npm run build && npm test
```

Make wrappers for convenience:
- `make lint` — Go + frontend linters
- `make test` — Go tests + coverage, no race, same as the CI gate
- `make test-race` — the race detector in CI's six shards (#2293)
- `make test-web` — `vitest` + coverage

What CI enforces (`.github/workflows/ci.yml`):

- Required to merge: `lint`, `validate (Go)`, `Security Summary`, `codecov/patch`. `validate (Go)` runs the `go test` line above, without `-race`.
- `validate (Go race)` runs on every PR in six shards but is not a required check. A red race shard won't block the merge, but treat it as a real failure; `make test-race` reproduces it locally.
- Named suites run as their own jobs, not as part of `go test ./...`: `tests/smoke` (`make smoke`) and `tests/abscontract` (`make abs-contract`) on every CI run, `tests/predeploy` (`make predeploy-smoke`) on `v*` tags against dev, and `tests/canary` nightly from `.github/workflows/canary.yml`.

For wiring-level changes (handlers, scheduler jobs, downloader integrations, import flows), unit tests aren't enough. Escalate to `make smoke` — see the `smoke-testing` skill for the decision tree.

## Pinned tool versions

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4
go install golang.org/x/vuln/cmd/govulncheck@latest
```

CI uses `golangci-lint v2.11.4` exactly. `@latest` locally can disagree with CI and waste a round-trip.

## Test patterns

**Backend (Go)**

- Tests go in the same package as the code they cover, usually starting from the sibling file (`internal/api/books.go` → `internal/api/books_test.go`). A source file may have several test files when the behaviours split cleanly; `internal/api` has far more test files than source files.
- Name test files and test functions after the behaviour they pin (`add_book_dedup_existing_test.go`, `TestAudiobookImportMode_AutoOverridesExternal`), never after a metric. Don't add new `*_coverage_test.go` or `*.coverage.test.tsx` files; the existing ones predate this rule.
- DB-backed tests use `db.OpenMemory()` from `internal/db`. Never use temp files; never mock the DB. Mocked DB tests pass when migrations are broken.
- HTTP handlers: `httptest.NewRecorder` + `httptest.NewRequest`, exercise routes through the same setup as production.
- Assertions on wrapped errors: `errors.Is` / `errors.As` (errorlint rejects `==`).
- Always check `rows.Err()` after iterating SQL rows (rowserrcheck enforces it).
- Outbound HTTP in tests: stub via `httptest.NewServer`, never live network.

**Frontend (React + TS)**

- Pages: `web/src/pages/<Name>Page.tsx` with sibling `<Name>Page.test.tsx`.
- `vitest` + `@testing-library/react` + `jsdom`; setup in `web/src/test-setup.ts`.
- Never hit the network. `web/src/test-setup.ts` installs an MSW (Mock Service Worker) server globally (`web/src/test/msw.ts`); prefer MSW handlers for new tests that exercise requests. Older tests `vi.mock` the API client in `web/src/api/`; both are in use.
- Interactions use `fireEvent` from `@testing-library/react`. `@testing-library/user-event` is not a dependency.
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

## Bug fixes need a regression test

A bug fix ships with a regression test that fails before the fix, and the PR body shows that failing output. If the fixed logic exists in more than one copy (two handlers, a Go and a TS implementation, one check per route), either fix every copy or add a test that covers all of them: a shared table run against each copy, or a test that walks every route.

## Common failure modes

| Symptom | Likely cause |
|---------|--------------|
| `gofmt` flags a file you didn't touch | Merge-base drift; run `gofmt -w <path>` |
| `govulncheck` fails with a CVE you didn't introduce | New advisory in a transitive dep. Bump the offender; call out the bump in the PR body |
| Every test in a package fails | A migration regression. Each package opens a fresh in-memory DB and runs all migrations — one broken migration takes the whole package down |
| `vitest` passes, `vite build` fails | Missing static asset import or type error `tsc --noEmit` didn't catch. Run typecheck + build together |
| Test passes locally, fails on CI with `-race` | A goroutine leak or unsynchronized map access. Run `make test-race`, or `-race -count=10` on the one package, to reproduce |
| Smoke suite hangs at startup | Binary panicking before binding. Run `./bindery` directly, read stderr |

## Don't

- Don't `t.Skip()` a flaky test to make CI green — investigate or revert the change that introduced flake.
- Don't mock `internal/db`. Use the in-memory DB.
- Don't add new custom build tags (`//go:build foo`). The only one is `canary` on `tests/canary/*.go`, which keeps live-provider tests out of `go test ./...`; they run with `go test -tags canary ./tests/canary/`. The other constraints in the tree are platform (`windows`, `unix`) and `race` / `!race` test helpers.
- Don't add `//nolint` without a reason on the same line; readers and CI both expect a justification.
- Don't commit `coverage.out` or `web/coverage/` — they're build artifacts.
