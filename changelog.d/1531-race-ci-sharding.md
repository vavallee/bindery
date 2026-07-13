### Fixed
- **Go race CI no longer times out** (#1531) — split the race suite into five
  parallel shards so the large API test package stays below its per-package
  deadline.
