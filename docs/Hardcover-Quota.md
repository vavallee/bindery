# Hardcover daily quota protection

Hardcover Free allows **5,000 requests/day** and Supporter **50,000/day**.
See the supplied [Hardcover Supporter comparison](https://hardcover.app/supporter).
Bindery detects the key's actual allowance rather than assuming every key is Free.

## Verified API contract

Implementation was checked against Hardcover's official
[Getting Started API documentation](https://github.com/hardcoverapp/hardcover-docs/blob/e8d38c8b7bd53cada7e97121ab0cacfa0804013c/src/content/docs/api/Getting-Started.mdx#rate-limits)
on 2026-09-14. The documentation website itself required a browser challenge;
the official source was readable. No authenticated live quota measurement was made.

The documented `"daily"` bucket in `RateLimit-Policy` supplies `q` (allowance)
and `w` (window seconds). The corresponding `RateLimit` bucket supplies `r`
(remaining) and `t` (seconds until reset). Bindery uses these current headers,
not the legacy burst headers, to detect allowance and availability. A `429`
can also carry `Retry-After`. Long waits are persisted without the transient
throttle's 30-second cap. A successful response with zero daily remaining
also stops subsequent requests. Every existing Bindery GraphQL operation has
one top-level field, so each HTTP attempt consumes one unit, including retries,
pages and account discovery. Nested selections do not add units.

## Configuration and status

`hardcover.daily_request_limit` is a positive integer fallback, default **5000**.
Set it to **50000** for Supporter when detection is unavailable, or to the actual
custom allowance agreed with Hardcover. The settings descriptor documents the
accepted range (1–1,000,000,000). Use the existing admin settings API:

```http
PUT /api/v1/setting/hardcover.daily_request_limit
Content-Type: application/json

{"value":"50000"}
```

Changes apply on the next request without restarting. A valid detected allowance
takes precedence and refreshes on responses, including upgrades and downgrades.
A new key gets fresh discovery. An already-known upstream exhaustion/hold remains
in force until its reported eligible time; changing the fallback does not bypass it.
After reset the next request can discover a changed plan.

Admins can inspect **`GET /api/v1/hardcover/quota`** without spending an API request.
It reports the configured key's effective allowance, remaining budget, usage
source, interactive reserve, deferred status and next eligible time. Source is
`upstream`, `local estimate (detected allowance)`, or
`local estimate (configured fallback)`. Upstream remaining includes usage that
other applications have already spent when the response was generated; a local
estimate cannot see those applications. The status endpoint describes interactive
eligibility; background work yields when remaining reaches the reported reserve.
Errors from attempted work also state the quota source and next eligible time.

When no valid reset/remaining data is available, Bindery counts attempts locally
in hourly buckets retained for **24–25 hours**, conservatively covering a rolling
day. This is a local protection policy, not a claim about Hardcover's reset
schedule. Invalid headers do not replenish an existing budget. A detected reset
uses its reported time rather than calculating a midnight boundary.

## Shared accounting and deferred work

The metadata provider, list sync, list browsing, and settings check share one
controller. Its requests are serialized so concurrent requests and out-of-order
responses cannot replenish or overspend its known budget. Accounting is written
to SQLite before transmission, and response state is saved before the next request.
A crash or uncertain transport failure can conservatively overcount an attempt.
Use one Bindery process per database; independent installations do not coordinate
their local counters, though each can consume Hardcover's authoritative headers.

A counted `me { id }` query identifies a newly seen key's account. Different keys
for the same verified account then share usage and cooldowns. Before that identity
is known, its discovery request necessarily uses a provisional key budget. If
`me` is unavailable (for example, a scope-restricted key), accounting remains
isolated by a SHA-256 key fingerprint and discovery is retried after 24 hours.
Use the same key across Bindery integrations in that case. Credentials are never
placed in accounting keys or logs. Internal accounting is hidden from the generic
settings API. Deferred edition work lives in a separate table tied to its book.
Deleting a book removes its pending work; late hydration cannot recreate it.
Migration 086 preserves live work from legacy markers and drops orphan markers.

Scheduled list sync, nightly author metadata refresh and bulk author catalogue
work reserve **10%** of the effective allowance for interactive requests (500
Free / 5,000 Supporter). Quota deferral does not become an empty-book result or
cause primary ISBN lookup to switch provider identity. Ordinary transient retries
remain available and each attempt is counted. Adaptive retry pacing is shared
per detected account (per key until identification), so one account’s transient
rejection does not pause unrelated accounts. Cache hits remain outside accounting;
this change adds no search caching or request deduplication (#2594).

List sync leaves its last-success timestamp untouched when deferred; the next
scheduled or manual run can retry safely. Author catalogue fetch/filter failures
leave existing records intact. Edition hydration retains a durable provider-ID
marker when quota prevents completion, including series additions, accepted
recommendations and book rebinding. The marker preserves the provider target and
pinned format choice; a later author catalogue refresh retries it and clears it
on success. Work for a book whose provider identity has since changed is discarded. Already-prefetched editions can still be applied,
but a known deferred prefetch is not retried live during hydration. Retrying after
eligibility uses the existing work entry points; this feature does not introduce
a separate immediate-resume job or change the configured sync schedule.
