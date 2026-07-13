### Fixed
- **Go race CI no longer times out** (#1531) — split the race suite into six
  parallel shards so the large API and database test packages stay below their
  per-package deadlines.
