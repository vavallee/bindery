---
name: fix-issue
description: Analyze and fix a GitHub issue end-to-end, producing a PR
disable-model-invocation: true
---

Fix GitHub issue $ARGUMENTS.

1. `gh issue view <n>` — read the issue and all comments. If reproduction steps are missing, say so and draft a comment asking for them (tone rules in CLAUDE.local.md) instead of guessing.
2. Search the codebase for the affected area; read before writing.
3. Write a failing test that reproduces the bug first, and keep its failing output for the PR body. No test = no fix. If the buggy logic exists in more than one copy, fix every copy or write a test that covers all of them (see the `testing` skill).
4. Implement the fix on a branch `fix/<n>-<slug>`. Address the root cause, not the symptom.
5. Security-sensitive area (auth, settings endpoints, user-scoped resources, proxy headers)? Check against the conventions in CLAUDE.md before proceeding.
6. Run the affected package tests, then lint. Fix what breaks.
7. Add a fragment under `changelog.d/` (format in `changelog.d/README.md`; preview with `make changelog`). Do not edit CHANGELOG.md directly — a maintainer assembles it at release time.
8. Push, open a PR targeting `main` with `Fixes #<n>`, summarize the root cause in the body.

Never tag a release from this workflow — releases follow the flow in CLAUDE.md and are Vincent's call.
