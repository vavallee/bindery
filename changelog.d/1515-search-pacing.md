### Fixed
- **Bulk searches no longer burst your indexers** (#1515) — "search all wanted"
  for a prolific author, filling a series, per-author auto-search, and the
  scheduled wanted loop now pace their indexer searches (a short gap between
  each) instead of firing as fast as slots free up. A 30-book author could
  previously flood a rate-limit-free Prowlarr into dropping requests, so
  nothing got grabbed; the fan-outs still run with the same concurrency caps
  but no longer sustain a tight query loop.
