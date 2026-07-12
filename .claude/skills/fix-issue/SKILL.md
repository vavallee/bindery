---
name: fix-issue
description: Analyze and fix a GitHub issue end-to-end, producing a PR
disable-model-invocation: true
---

Fix GitHub issue $ARGUMENTS.

1. `gh issue view <n>` — read the issue and all comments. If reproduction steps are missing, say so and draft a comment asking for them (tone rules in CLAUDE.local.md) instead of guessing.
2. Search the codebase for the affected area; read before writing.
3. Write a failing test that reproduces the bug first. No test = no fix.
4. Implement the fix on a branch `fix/<n>-<slug>`. Address the root cause, not the symptom.
5. Security-sensitive area (auth, settings endpoints, user-scoped resources, proxy headers)? Check against the conventions in CLAUDE.md before proceeding.
6. Run the affected package tests, then lint. Fix what breaks.
7. Add a CHANGELOG.md entry under Unreleased.
8. Push, open a PR targeting `main` with `Fixes #<n>`, summarize the root cause in the body.

Never tag a release from this workflow — releases follow the flow in CLAUDE.md and are Vincent's call.
