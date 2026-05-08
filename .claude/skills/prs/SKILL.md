---
name: prs
description: Use when opening or updating a pull request, responding to review, or filing a GitHub issue — covers the PR body skeleton matched to recent merged PRs, draft → ready → squash mechanics, rebase-vs-merge policy, and the issue-template routing (`bug_report.yml` / `feature_request.yml`, blank issues disabled).
---

# PRs and issues

## PR body skeleton

Base template: `.github/pull_request_template.md`. For non-trivial PRs expand it. The pattern below matches recent merged PRs (#459, #467):

```markdown
## Summary

<1–3 sentences on what changed and why. End with "Closes #NN." when applicable.>

<For larger PRs add structural sections — pick what's relevant:>
## Implementation notes
## Where it's mounted / how it's wired
## Why minimal / scope decisions
## Dependencies        <!-- if go.mod or package.json gained anything -->
## Suggested review order   <!-- numbered list — only for large PRs -->
## Follow-ups (not in this PR)

## Checklist

- [ ] Tests added or updated
- [ ] Doc-update gate cleared (see `commits` skill — `docs/`, `README.md`, godoc, Helm values)
- [ ] Wiki pages updated if user-facing behaviour changed

## Test plan

- [ ] `go test ./cmd/... ./internal/...`
- [ ] `cd web && npm run build`
<!-- add the suites you actually ran:
- [ ] `go test -count=1 ./tests/abscontract/...`
- [ ] `cd web && npm test -- SomePage.test.tsx`
-->
```

**Tone:** state the *why*. The maintainer favours tables for enumerating fields, metrics, or labels — match that style. Mark each checklist item `[x]` only after running it; an unchecked box is honest, a wrongly-checked one is worse than not checking.

`CHANGELOG.md` is **not** in the PR checklist — it's authored at release time only. See the `tag-release` skill.

## PR mechanics

- **Open as Draft** while iterating. Mark Ready for Review only after the diff is final and all checklist items are checked.
- **Self-review the diff** in the PR UI before requesting review — you'll catch things `git diff` won't (missed renames, debug prints, accidental file additions).
- **Don't `git merge main` into a feature branch.** Rebase: `git fetch origin && git rebase origin/main`. Keeps history linear and the diff readable.
- **Resolve review comments.** Either fix them, or reply with reasoning. Don't silently resolve threads.
- **Squash at merge** for single-purpose PRs (the default expectation here). Keep history (rebase merge) only when commits are individually atomic, each compiles, and each adds reviewable value.
- **Tag link discipline:** Use `Closes #NN` in the PR body to auto-close the issue on merge. Use `Refs #NN` for related-but-not-closing.

## Issues

`blank_issues_enabled: false` — every issue must use a template.

| Type | Template | Required fields |
|------|----------|-----------------|
| Bug | `.github/ISSUE_TEMPLATE/bug_report.yml` | Pre-flight (search + latest), Current behaviour, Expected behaviour, Steps to reproduce, **Trace logs at DEBUG** (issues without these may be closed without investigation), Deployment method (Docker / Helm / Binary / Source), Bindery version |
| Feature | `.github/ISSUE_TEMPLATE/feature_request.yml` | Pre-flight (search + roadmap check), Problem, Proposed solution. Optional: Alternatives considered, Additional context |

Routing:
- Support / questions → [GitHub Discussions](https://github.com/vavallee/bindery/discussions), not issues.
- Security vulnerabilities → [Security Advisory](https://github.com/vavallee/bindery/security/advisories/new), not issues.
- For non-trivial features, **open the issue before starting work** so scope can be aligned (README explicitly asks for this).

## Don't

- Don't open a public issue for a security vulnerability — use Security Advisory.
- Don't push tags (`v*`); releases are gated by the maintainer (see `tag-release` skill).
- Don't force-push without explicit user instruction. After review starts, prefer fixup commits over `--force-with-lease` on the branch.
- Don't open a giant PR without flagging the size in the body and proposing a review order (#459 is the model for how to handle a necessary big PR).
