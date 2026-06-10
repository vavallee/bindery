### Added
- **Log-rate Discord alerting (Helm)** (#1085) — new opt-in `logAlert` CronJob in the chart polls `/api/v1/system/logs` and pings a Discord webhook when WARN/ERROR counts in the lookback window cross configurable thresholds, so persistent log noise pages someone instead of sitting unseen.
