### Changed
- **The telemetry dashboard caches its numbers for a minute.** getbindery.dev/stats and `/api/stats` now share one computed snapshot for up to 60 seconds instead of rerunning every dashboard query on each page load, and visitors who arrive at the same moment share one computation. The figures can lag a new ping by up to a minute. `/stats.json` and ping handling are unchanged.
