---
name: tag-release
description: Use when cutting a release — composing the `## [vX.Y.Z]` `CHANGELOG.md` section, picking the version bump, walking commits since the last tag, and pushing the tag. Pushing the tag triggers GoReleaser + provenance signing, a public GitHub Release and Discord announcement, and an ArgoCD deploy of the maintainer's instance.
---

# Tag a release

## Scope

This skill drafts the release artifacts **and pushes the tag**. Land the CHANGELOG entry on `main` first, then tag a commit that contains it.

Pushing a `v*` tag runs the whole pipeline. From `.github/workflows/ci.yml`:

- `image` — Docker build/push, then bumps `values-dev.yaml` on `development` (dev refresh).
- `goreleaser` — binaries + SBOMs → **public GitHub Release**.
- `predeploy-smoke` — gated on `v*`.
- `deploy-prod` — gated on `v*` + a green `goreleaser`; bumps `charts/bindery/values.yaml` on `main`, which the ArgoCD "prod" app syncs. Despite the name this is the maintainer's own instance, not a customer fleet — a bad deploy is their problem to roll forward, not an outage.
- `notify-discord` — posts a **public announcement** to the Discord server.

The genuinely outward-facing parts are the GitHub Release and the Discord post: other people download those binaries and read that announcement. The deploy itself is low-stakes. So don't ceremonialise tagging — but do mention any known-unverified fix riding along, since that lands in public release notes.

The deploy bot owns the `chore(deploy): promote bindery to vX.Y.Z [skip ci]` commit — never write that by hand.

## Picking the version

SemVer per the CHANGELOG header reference:

| Change shape | Bump |
|--------------|------|
| Backwards-incompatible API/config (env var renamed, removed feature, breaking schema migration) | major |
| New feature, new env var, new endpoint | minor |
| Bug fixes, doc-only updates, security backports | patch |

## Walking the diff

From the previous tag to `HEAD`:

```bash
PREV=$(git describe --tags --abbrev=0)
git log --oneline --no-merges "$PREV"..HEAD
git log "$PREV"..HEAD -- CHANGELOG.md docs/ README.md   # changes already documented
```

Group commits by Conventional Commits type → CHANGELOG sub-heading:

| Commit type | CHANGELOG section |
|-------------|-------------------|
| `feat` | **Added** |
| `perf`, behaviour-changing `chore` | **Changed** |
| `fix` | **Fixed** |
| `docs` (user-facing only) | **Docs** |
| Removals / deprecations | **Removed** |

Skip release-internal commits (`chore(release)`, `chore(deploy)`, `[skip ci]` from bots).

## Maintainer style

Read the most recent two `## [vX.Y.Z]` sections in `CHANGELOG.md` before drafting:

- Long, explanatory bullets — not one-liners. State the *user-visible behaviour change*, then the *why* and the *internal mechanism*. Examples in `CHANGELOG.md` v1.4.0–v1.4.3 are the model.
- Reference PR numbers (`(#NN)`) and `closes #NN` when applicable.
- Code identifiers, env vars, and file paths in backticks. Bold the leading phrase of each bullet.
- Date in `YYYY-MM-DD`, line below the heading.

## Pre-tag checklist

- [ ] CHANGELOG section drafted with the right version, date, and groupings.
- [ ] Every PR merged since `$PREV` is represented (or deliberately skipped — internal-only refactors).
- [ ] Version bump aligns with SemVer rules above (no surprise majors hidden in minor bumps).
- [ ] `docs/upgrade-v2.md` extended if any breaking-change behaviour shipped.
- [ ] CHANGELOG entry is merged to `main` — the release step reads it at the tagged commit and aborts if missing.
- [ ] `main` is green (build, backend suite, frontend) at the commit being tagged.
- [ ] Any known-unverified fix in the release is called out to the maintainer before pushing.

## Tagging

Annotated tag on a commit that contains the CHANGELOG entry, then push:

```bash
git tag -a vX.Y.Z <commit> -m "vX.Y.Z"
git push origin vX.Y.Z
```

Then watch the run — `deploy-prod` and `notify-discord` are the ones that matter:

```bash
gh run list --workflow=ci.yml --limit 3
gh run watch <run-id>
```

If the release fails after `deploy-prod` has already run, fix forward with a patch release; don't delete and re-push the tag.

## Don't

- Don't tag a commit whose CHANGELOG entry isn't merged to `main` — the release step aborts.
- Don't delete or force-move a pushed tag to "fix" a release; cut a patch release instead.
- Don't edit `charts/bindery/values.yaml` image digest — auto-bumped by the deploy bot per `[skip ci]` commits.
- Don't compose CHANGELOG entries during ordinary feature work — only at release time. The `commits` skill explicitly defers CHANGELOG to this skill.
