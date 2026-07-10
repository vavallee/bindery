---
name: discord-reports
description: Pull user bug reports from the bindery Discord and convert them to GitHub issues
disable-model-invocation: true
---

Collect and process Discord bug reports. Access details (bot token location, guild/channel IDs) are in CLAUDE.local.md — never print the token.

1. Fetch recent messages from the report channels: `GET /channels/{id}/messages?limit=50`, header `Authorization: Bot <token>`, Discord API v10. Use `curl` — Discord's Cloudflare blocks Python urllib. Default lookback: since the last run noted in memory, else 7 days; $ARGUMENTS overrides.
2. Filter to actual reports: errors, unexpected behavior, version/setup problems. Skip chatter and feature musings unless substantial.
3. Dedupe against existing issues (`gh issue list --search`). If a report matches an open issue, note the +1 instead of filing a duplicate.
4. For each new report, draft a GitHub issue: title, what the user saw, version if stated, link to the Discord message. Show me all drafts before creating anything.
5. After I approve: `gh issue create`, then draft a short reply for the Discord thread pointing to the issue (tone rules in CLAUDE.local.md). Post replies only after I approve them.
6. Record the sweep timestamp in memory so the next run picks up where this left off.
