### Changed
- **Forks no longer run upstream-only workflow jobs** — image publishing, GoReleaser, the Discord release post, and the nightly telemetry backup, ping drift and AI backlog sweep jobs are now skipped outside `vavallee/bindery` instead of failing on every sync.
