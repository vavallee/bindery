---
name: commits
description: Use when picking a branch name, staging files, or writing a commit message — covers branch conventions, the project's Conventional Commits format with the scope vocabulary observed in history, and the documentation-update gate that fires before every commit (`docs/*`, `README.md`, godoc, Helm values).
---

# Commits

## Branches

| Use | Form |
|-----|------|
| Feature work | `feature/<short-slug>` |
| Bug fix tied to an issue | `fix/<NN>-<short-slug>` (NN = issue number) |
| Codex / agent branches | `codex/<slug>` |
| Release / deploy | bot-owned — never push manually |

Branch from `main`, not `development` (no `development` branch in this repo). Keep one branch focused on one change. If you find yourself fixing two unrelated things, that's two branches.

## Commit messages — Conventional Commits

```
<type>(<scope>): <imperative subject, lowercase, no trailing period>

<optional body — explain *why*, not *what*. Wrap at ~72 cols.>
```

**Types** observed in history: `feat`, `fix`, `chore`, `docs`, `test`, `perf`. The repo doesn't use `refactor`, `style`, or `ci` as types — `ci` is a *scope*.

**Scopes** (use one of these when it fits; invent sparingly):

| Layer | Scopes |
|-------|--------|
| Backend domain | `api`, `auth`, `oidc`, `db`, `metadata`, `indexer`, `downloader`, `importer`, `recommender`, `scheduler`, `notifier`, `series`, `author`, `abs`, `calibre`, `telemetry`, `opds`, `httpsec` |
| Cross-cutting | `ci`, `release`, `deploy`, `lint`, `deps` / `deps-dev`, `migrate`, `sync`, `scanner`, `search` |
| Other | `test`, `docs`, `web` |

**Issue link** in the subject: append `(#NN)` — e.g. `fix(indexer): strip possessive author prefix from title before relevance matching (#446)`.

**Reserved:** `chore(deploy): promote bindery to vX.Y.Z [skip ci]` is bot-only. Never include `[skip ci]` in human or agent commits — it bypasses CI and CI is the gate.

**Subject examples from real history:**

- `feat(oidc): test discovery button surfaces IdP errors before login (#460)`
- `fix(recommender): don't gate author-new / series / genre-popular on ratings count`
- `perf(author): parallelize author work discovery and searches`
- `test(downloader): cover download client edge-case matrix (#451)`
- `docs(series): document enhanced hardcover series workflow`

## Documentation-update gate

Before staging the commit, walk the diff and update the matching docs in the *same* commit (or at minimum the same PR — never a follow-up).

| If your diff touches… | Update… |
|-----------------------|---------|
| Env vars, config, startup flags, upgrade path | `docs/DEPLOYMENT.md` |
| Auth modes / OIDC / proxy auth | `docs/auth-multiuser.md`, `docs/auth-oidc.md`, `docs/auth-proxy.md`, `docs/troubleshooting-auth.md` as applicable |
| ABS import flow | `docs/abs_import.md` (+ `docs/ABS-Import-Wiki.md` for user-facing changes) |
| Hardcover / series feature | `docs/Hardcover-Series-Wiki.md` |
| Upgrade-from-v1 path | `docs/upgrade-v2.md` |
| Multi-user behaviour | `docs/multi-user.md` |
| Feature surface advertised at top level | `README.md` |
| Helm chart values, env, image config | `charts/bindery/values.yaml` |
| New exported Go symbol with non-obvious behaviour | godoc comment on the symbol |
| Agent process / lifecycle / skill triggers | `AGENTS.md` (only when the agent contract genuinely changes — don't routinely touch) |

**Rule of thumb:** the entire `docs/` directory and `README.md` are maintained at every commit — they should never lag behind the code. `AGENTS.md` is updated only when contributor / agent process actually shifts. `CONTRIBUTING.md` is human-maintained — don't auto-edit unless the user asks.

Pure refactors with no behaviour change require no doc update.

## Out of scope (release-time only)

`CHANGELOG.md` sections are authored at tag/deploy time, not at commit time. Don't add an `[Unreleased]` entry; don't modify versioned sections. The **`tag-release`** skill covers CHANGELOG authoring when cutting `## [vX.Y.Z]`.

## Don't

- Don't include `[skip ci]` in human or agent commits.
- Don't `--amend` after a hook failure — the commit didn't happen, so amend modifies the *previous* one. Re-stage and create a new commit.
