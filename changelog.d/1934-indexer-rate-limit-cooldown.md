### Fixed
- **A rate-limited indexer is left alone until it says to come back**
  ([#1934](https://github.com/vavallee/bindery/issues/1934)) — when an indexer
  answers a search with a Newznab 500 (`Request limit reached. Retry in 485
  minutes.`) Bindery used to record nothing, so the next search and every search
  for the following eight hours sent it another request it had already refused.
  The rate-limit classification existed but was consulted only inside a single
  search, to stop the query cascade falling through to lower tiers. The retry
  hint is now parsed out of the indexer's own message and that indexer is
  skipped until the deadline passes, across the scheduled wanted scan, on-add
  and bulk searches, and interactive search alike — they share one searcher, so
  a limit hit by one is respected by all of them. An indexer that gives no hint
  gets an hour; a parsed hint is clamped to between a minute and a day so a
  malformed or absurd value cannot bench an indexer indefinitely. Editing the
  indexer clears the hold immediately, so a new API key or a different account
  takes effect on the next search rather than waiting out a lockout that
  belonged to the old configuration. Interactive search reports the held indexer
  as skipped with the deadline in the Search details panel, in the same place
  the original error appeared, rather than dropping it from the list.

  Two related gaps are deliberately untouched here and tracked separately:
  authentication failures (a suspended account, a revoked key) get no cooldown,
  because they never heal on a timer and a user who fixes their key must see it
  work immediately — those need visibility and a notification
  ([#1935](https://github.com/vavallee/bindery/issues/1935)) — and auto-grab
  still decides from whatever indexers answered without recording that the
  others were unreachable
  ([#1936](https://github.com/vavallee/bindery/issues/1936)). The cooldown is
  held in memory, so a restart costs one refused request per indexer per search
  before it re-learns the limit, which is exactly the old behaviour and never
  worse.
