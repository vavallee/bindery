---
name: smoke-testing
description: Use when deciding whether to run an out-of-process test suite (real binary or live instance) — picks between `make smoke`, `make abs-contract`, and `make predeploy-smoke` after the unit suite passes but you've changed handler wiring, scheduler jobs, downloader integration, ABS imports, or anything that boots the binary. For the routine pre-PR matrix (lint / vet / test / typecheck / build) see the `testing` skill.
---

# Smoke testing Bindery

Bindery has three out-of-process test layers in `tests/` that complement `go test ./internal/...`. Picking the wrong one wastes time; picking none and shipping wastes more.

## Decision: which suite?

| Suite | Command | When to run | Cost |
|-------|---------|-------------|------|
| Unit + integration | `make test` | Every change. In-memory SQLite via `db.OpenMemory`. | Seconds |
| HTTP smoke (real binary) | `make smoke` | Before opening a PR that touches handlers, routing, auth, scheduler wiring, embed, or `cmd/bindery/`. Boots the actual binary on a real port. | ~30–60s; requires `make build` first (the target chains it) |
| ABS contract | `make abs-contract` | Touching anything under `internal/abs/`, `tests/abscontract/`, or the ABS import flow. Pinned upstream contract — flakes here usually mean upstream drift, not your bug. | Up to 15 min |
| Predeploy | `make predeploy-smoke` | Only against a live instance. Requires `BINDERY_URL` and `BINDERY_API_KEY` env vars. Use after a deploy, not during development. | ~2 min, needs network |

Default: `make test` is sufficient for most diffs. Promote to `make smoke` when you've changed wiring, not just logic.

## Before you run anything

```bash
# Smoke needs the binary built and the embedded SPA in place.
make build       # equivalent: web-build → copy dist → go build
```

`make smoke` chains `make build` automatically — but if you've only edited Go and want to skip the slow `npm ci`, build once and re-run `go test -count=1 -timeout=60s ./tests/smoke/...` directly.

## Reading failures

- **Smoke suite hangs at startup** — usually the binary is panicking before it binds. Run `./bindery` directly and read stderr.
- **Smoke suite passes but unit tests fail** — the in-memory and on-disk DBs disagree. Check for a missing migration or for a test that depends on real-clock timing.
- **ABS contract reports diffs against the pinned snapshot** — first verify it isn't upstream drift (`tests/abscontract/` has fixture timestamps); only adjust the snapshot if the change is intentional.
- **Predeploy fails with 401/403** — the API key isn't being accepted. Check `X-Api-Key` header reaches the server (reverse-proxy strip is the usual cause).

## Pre-PR sequence

For changes touching the API surface, run, in order:

```bash
make lint
make test
make smoke
cd web && npm run typecheck && npm run build && cd ..
```

Match what CI will run — see CONTRIBUTING.md §"Running the full local check suite" for the full matrix including `govulncheck`, `golangci-lint v2.11.4` pinning, and the Docker buildx rehearsal.

## Don't

- Don't add new fixtures to `tests/abscontract/` casually — they pin to upstream behaviour and exist to catch regressions, not to satisfy a flaky run.
- Don't run `make predeploy-smoke` from a feature branch against production. It hits real endpoints.
- Don't commit a passing `make smoke` log if `make test` is failing — the smoke suite covers golden paths, not error paths.
