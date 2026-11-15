---
name: tag-release
description: Use when cutting a release — composing the `## [vX.Y.Z]` `CHANGELOG.md` section, picking the version bump, walking commits since the last tag, and pushing the tag. Pushing the tag triggers GoReleaser + provenance signing, a public GitHub Release and Discord announcement, and an ArgoCD deploy of the maintainer's instance.
---

# Tag a release

## Scope

This skill drafts the release artifacts **and pushes the tag**. Land the CHANGELOG entry on `main` first, then tag a commit that contains it.

Pushing a `v*` tag runs the whole pipeline. From `.github/workflows/ci.yml`:

- `image`: needs `test`. Docker build/push for linux/amd64 + linux/arm64, including `:latest`. It does not wait for the jobs below, so a tag whose later jobs fail has still moved `:latest`. It does not touch dev: `deploy-dev` runs only for the `development` branch.
- `goreleaser`: needs `test`. Binaries + SBOMs → **public GitHub Release**.
- `predeploy-smoke`: gated on `v*`, needs `image`.
- `deploy-prod`: gated on `v*`, needs `image`, `goreleaser` and `predeploy-smoke`. Sets `image.tag` in `charts/bindery/values.yaml` on `main` to the release version, which the ArgoCD "prod" app syncs. Despite the name this is the maintainer's own instance, not a customer fleet, so a bad deploy is theirs to roll forward.
- `notify-discord`: needs `image` and `goreleaser`. Posts a **public announcement** to the Discord server.

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
- [ ] **Every reporter and outside contributor behind a fix is credited by name.** Credit was routine through v1.30.2 and then lapsed for five straight releases. Fragments often carry the credit already, so check it survived the rewrite when assembling: the v1.33.1 assembly dropped "Thanks ianepreston for both reproductions" and left the person who reported eight of its ten fixes unnamed. Discord handles are fine; older entries credit cleb, PixieApples, Daize and others who have no GitHub account.
- [ ] **Anything cut from this release is checked against what is still shipping.** For every item deferred during planning, ask whether something in the release depends on it. Deferring a fix is fine; deferring it in the release that ships the feature which needs it is how v1.33.0 shipped #1780's unconditional box-set prune on the ingestion path while leaving series fill unfiltered (#2239).
- [ ] **A fix to shared state or shared data has had its sibling consumers grepped.** #2188 wired supplement ranking into the scan loop's two call sites and missed `LibrarySnapshot.FindExisting`, which consumes the same walk (#2240). Same shape as the #2211/#2216 import break. Find every caller, not just the one in the bug report.
- [ ] **A re-landed revert has been re-tested against whatever changed underneath it.** #2041 was held back by #2093 specifically so it would not ship beside the metadata profile filters. When it re-landed in v1.33.0 its own guardrail tests still passed, but nobody exercised it against those filters, which is exactly where it broke (#2241).
- [ ] **The build has soaked on dev.** A `v*` tag goes straight to prod (`deploy-dev` runs only for the `development` branch, never on a tag), so tagging immediately after the last merge means the first real exercise of the release is by users. v1.33.0 drew ten issues within twelve hours, every one of them findable by leaving it on dev overnight. For a minor, sleep on it. For a patch fixing a live incident, say plainly that you are skipping the soak and why.

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
- Don't edit the `charts/bindery/values.yaml` image tag by hand. The deploy bot sets it at each release in a `[skip ci]` commit.
- Don't compose CHANGELOG entries during ordinary feature work — only at release time. The `commits` skill explicitly defers CHANGELOG to this skill.
