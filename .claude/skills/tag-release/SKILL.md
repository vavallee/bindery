---
name: tag-release
description: Use when cutting a release — composing the `## [vX.Y.Z]` `CHANGELOG.md` section, picking the version bump, and walking commits since the last tag. Authoring only; the maintainer pushes the tag (which triggers GoReleaser + provenance signing) and the deploy bot auto-bumps Helm `values.yaml`.
---

# Tag a release

## Scope

This skill drafts release artifacts. It does **not** run `git tag`, `git push --tags`, or any deploy. The maintainer pushes tags; the deploy bot owns `chore(deploy): promote bindery to vX.Y.Z [skip ci]`.

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
- [ ] Maintainer reviews and pushes the tag — agent stops here.

## Don't

- Don't `git tag` or `git push --tags`. The maintainer does this.
- Don't edit `charts/bindery/values.yaml` image digest — auto-bumped by the deploy bot per `[skip ci]` commits.
- Don't compose CHANGELOG entries during ordinary feature work — only at release time. The `commits` skill explicitly defers CHANGELOG to this skill.
