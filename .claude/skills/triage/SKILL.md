---
name: triage
description: Sweep open issues and PRs, categorize, and propose a work queue
disable-model-invocation: true
---

Triage the repo. Use subagents for the bulk reading; report summaries only.

1. `gh issue list --state open --limit 100` and `gh pr list --state open` — plus `gh pr checks` on anything marked ready.
2. Bucket issues: bug (reproducible), bug (needs info), feature request, question, stale. Flag anything security-relevant per CLAUDE.md conventions as top priority.
3. For needs-info issues, draft reply comments (tone rules in CLAUDE.local.md — brief, casual, human, no dashes in prose). Show me drafts before posting.
4. For open PRs: which are green and reviewable, which have failing checks, which conflict with main.
5. Output: a ranked work queue — quick wins first, then impact-ordered. One line each: number, title, bucket, why it's ranked there.

Don't start fixing anything from this skill; hand items to /fix-issue individually so each gets its own branch and clean context.
