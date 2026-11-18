---
name: autofix
description: Fix one confirmed bug unattended and leave a reviewable branch — failing test first, minimal diff, CI gate, changelog fragment, PR_BODY.md. Committing, pushing, and opening the PR happen outside this skill.
disable-model-invocation: true
---

# Autofix

Fix the confirmed bug in issue $ARGUMENTS.

You are running headless in a git worktree. You have no `gh`, no network for GitHub, and no commit/push. Your entire output is: a code change, a test that failed before it and passes after, a `changelog.d/` fragment, any doc updates, and `PR_BODY.md` at the repo root. A separate non-agent step commits, pushes, and opens the PR — attempting those yourself burns turns on a denied tool.

Work from the issue text you were given plus the code. You cannot fetch the issue, so if the report is too thin to reproduce, that is a stop condition (step 3).

## 1. Locate

Grep and read before writing. Repo map is in `AGENTS.md`; handlers are `internal/api/<resource>.go`, domain logic is `internal/{indexer,downloader,importer,metadata,recommender,scheduler,notifier}/`, DB access is `internal/db`, frontend is `web/src/`.

## 2. Reproduce before fixing

Write the failing test **first**, in the sibling test file (`internal/importer/flatten.go` → `internal/importer/flatten_test.go`; `web/src/pages/BooksPage.tsx` → `web/src/pages/BooksPage.test.tsx`).

```bash
go test ./internal/<pkg> -run TestYourNewTest -count=1 -v
npm test --prefix web -- YourComponent.test.tsx     # frontend
```

Run it and read the output. It must fail **for the reason in the issue** — a wrong value, a wrong status code, a wrong row. A test that fails because it doesn't compile, panics on nil, or asserts something unrelated proves nothing. Keep the exact failure output; it goes in `PR_BODY.md`.

If you cannot make a test fail, you have not found the bug. Stop (step 3) — do not "fix" code on a theory. A fix with no test that failed beforehand is not a fix. If the buggy logic exists in more than one copy, fix every copy or write a test that covers all of them (see the `testing` skill).

Test patterns are in the `testing` skill: `db.OpenMemory()` for DB-backed tests (never mock `internal/db`), `httptest` for handlers, no live network, `vitest` + `@testing-library/react` for the frontend.

## 3. Stop conditions

As soon as you know the root cause, check this list. If any line matches, write `PR_BODY.md` in the **Blocked** form (step 8) and stop without changing code. Stopping cleanly is a good outcome; a half-finished fix on a hard problem is not — you are cleared for small blast radius only.

| Stop if the fix would… | Why |
|---|---|
| Touch `internal/auth/**`, `internal/api/auth.go`, `internal/httpsec/**`, or user-scoped filtering (`owner_user_id`) | `SECURITY.md` and `CLAUDE.md` route auth/authz, SSRF, and cross-user visibility to a human |
| Edit an existing file in `internal/db/migrations/` | Migrations are forward-only; a shipped migration is immutable. Needing a *new* migration file is itself a sign the change is too big for this path |
| Change `go.mod`, `go.sum`, `web/package.json`, or `web/package-lock.json` | Dependency changes are gated by `govulncheck` and dependency-review, and you cannot run `govulncheck` here |
| Touch `.github/workflows/**` | The push token has no `workflow` scope — the push is rejected outright |
| Span more than a handful of files, or need a redesign / new abstraction | Out of scope for an unattended run; say so and let a human take it |

## 4. Fix

Change the minimum that makes the failing test pass. Address the root cause, not the symptom.

- No drive-by refactors, no renames, no reformatting lines you didn't otherwise change, no "while I'm here" cleanups. `CONTRIBUTING.md` is explicit that PRs which narrow the diff are preferred over PRs that add surface area — a reviewer reading an autofix PR should be able to hold the whole diff in their head.
- Wrap errors with `%w`, compare with `errors.Is`/`errors.As` (`errorlint` rejects `==`).
- Check `rows.Err()` after iterating SQL rows (`rowserrcheck` enforces it).
- Don't add `//nolint` without a same-line reason. Prefer fixing the finding.
- Add the error-path case to your test if the fix introduces a new branch.

Then re-run the test from step 2 and confirm it passes.

## 5. The gate

These are the CI jobs that must be green — `lint` and `validate (Go)` are required to merge (plus `Security Summary`, which you can't influence). Run them from the repo root, exactly:

```bash
gofmt -l .                                    # must print nothing (golangci-lint enables the gofmt formatter)
go vet ./...
golangci-lint run --timeout=5m ./...          # CI pins v2.11.4
go test -coverprofile=coverage.out -covermode=atomic ./cmd/... ./internal/...
```

Only if you touched `web/`:

```bash
npm ci --prefix web
npm run lint --prefix web
npm run typecheck --prefix web
npm run build --prefix web
npm test --prefix web
```

`make check` is **not** the gate. It runs `go test -race`, which is a different (slower, flakier) job than `validate (Go)`, and it omits `govulncheck`, which the `lint` job runs. Use the commands above.

You cannot run `govulncheck` — the only way this run breaks it is a dependency change, which step 3 already forbids.

If the fix changes handler wiring, scheduler jobs, or the import path, add `make smoke` (boots the real binary against `tests/smoke/`). Read `.golangci.yml` before suppressing any lint finding — most common warnings already have project-wide exclusions.

`coverage.out` is gitignored; leave it. Don't commit `web/coverage/`.

## 6. Changelog fragment

Create `changelog.d/<issue>-<slug>.md` — one fragment, new file, never an edit to `CHANGELOG.md` (that is assembled by the maintainer at release time). Format is in `changelog.d/README.md`:

```markdown
### Fixed
- **Retrying a failed import clears the old error instead of showing it forever** (#1633) — `retry-import` reset the retry count and the status but left `error_message` untouched, so a queue row that recovered kept rendering a stale error next to an import that had demonstrably succeeded. The retry now clears `error_message` in the same statement that re-arms the row.
```

- First line is the Keep-a-Changelog section: `### Added`, `### Changed`, `### Fixed`, `### Removed`, `### Security`, `### Deprecated`. A bug fix is `### Fixed`.
- Bold the user-facing effect, then `(#<issue>)`, then an em dash and what was actually wrong and what now happens. Write it as release notes a user reads — not "fixed a bug in scanner.go".
- Preview with `make changelog`.

## 7. Docs

Walk your own diff and update the matching docs in the same change — never a follow-up. Full matrix is in the `commits` skill; the rows that realistically fire on a bug fix:

| Diff touches | Update |
|---|---|
| Env vars, config, startup flags, upgrade path | `docs/DEPLOYMENT.md` |
| ABS import behaviour | `docs/abs_import.md` |
| Hardcover / series behaviour | `docs/Hardcover-Series-Wiki.md` |
| Multi-user behaviour | `docs/multi-user.md` |
| A feature the README advertises | `README.md` |
| Helm values / env / image config | `charts/bindery/values.yaml` |
| Exported Go symbol whose documented behaviour changed | its godoc comment |

A fix that restores documented behaviour usually needs no doc change — the docs were already right. Say that in `PR_BODY.md` rather than leaving the reviewer to guess. Don't touch `CONTRIBUTING.md` or `AGENTS.md`.

## 8. `PR_BODY.md`

Write it at the repo root. Leave staging to the wrapper — in particular never `git add PR_BODY.md`; it is not gitignored and must not land in the commit.

```markdown
## Summary

<1–3 sentences: the symptom, the root cause, the change. Fixes #<n>.>

## Root cause

<What was actually wrong, in the code. Name the function and file. Explain why
it was not caught before — that is usually the most useful sentence for a reviewer.>

## What changed

<The diff in prose, one bullet per file. Note anything deliberately left alone.>

## Test plan

- [x] `go test ./internal/<pkg> -run <TestName> -count=1` — fails before the fix with:
      `<paste the exact failure line>`
- [x] `go test -coverprofile=coverage.out -covermode=atomic ./cmd/... ./internal/...`
- [x] `golangci-lint run --timeout=5m ./...`
- [x] `gofmt -l .` (clean)
<!-- frontend rows only if web/ changed -->

## Docs and changelog

- Changelog fragment: `changelog.d/<issue>-<slug>.md`
- Docs: <file(s) updated, or "no doc change — behaviour now matches what docs/X.md already describes">

## Self-review — what I'm least sure about

<Honest and specific. Name the assumption you'd want checked first, the case your
test does not cover, and anything you changed without being able to run it.
"Nothing" is almost never the true answer; if it genuinely is, say why.>

---

🤖 This change was written by an automated agent (bindhelper autofix) and has not
been reviewed by a human. The commit carries the maintainer's identity because of
how the branch is pushed — authorship is disclosed here. Please review the diff
and the reasoning above before merging.
```

Mark a checklist box `[x]` only after actually running that command. An unchecked box is honest; a wrongly checked one is worse than no box.

**Blocked form** — when a step-3 condition fired, or you could not reproduce. Replace the whole body with:

```markdown
## Blocked — no fix attempted

**Issue:** #<n>

**What I found:** <root cause as far as you got, with file/function names>

**Why I stopped:** <which stop condition, quoted from the skill, or "could not
reproduce: <what you tried>">

**What a human would need to do:** <the shape of the real fix, if you know it>

---

🤖 Investigated by an automated agent (bindhelper autofix). No code was changed.
```

Leave the working tree clean in that case. You have no `git restore`/`git checkout`, so undo exploratory edits by hand with Edit/Write and confirm with `git status` (only `PR_BODY.md` should be untracked).

## Do not

- **Don't `git commit`, `git push`, or use `gh`.** A separate non-agent step does all three. Any attempt is a denied tool and a wasted turn.
- **Don't edit anything under `.github/workflows/`.** The push token has no `workflow` scope; the push would be rejected.
- **Don't edit a shipped migration** in `internal/db/migrations/`. Forward-only, no exceptions.
- **Don't touch `internal/auth/`, `internal/api/auth.go`, `internal/httpsec/`, or user-scoped filtering.** Those go to a human per `SECURITY.md`.
- **Don't edit `CHANGELOG.md`.** Fragments only.
- **Don't edit `internal/webui/dist/`.** Generated from `web/`.
- **Don't bump dependencies** or run `go get`/`go mod tidy`.
- **Don't `t.Skip()` a failing test** or add `-skip` to make the gate green. If an existing test fails because of your change, either your fix is wrong or the test encoded the bug — say which in `PR_BODY.md`.
- **Don't reformat, rename, or reorganise** anything the fix doesn't require.
- **Don't file a public issue for a security finding.** Surface it in `PR_BODY.md` instead; disclosure flow is in `SECURITY.md`.
