### Fixed
- **Telemetry ping server daily snapshot no longer fails with "database is locked"** — the snapshot now takes the database write lock when it starts, so a ping arriving mid snapshot waits instead of making the snapshot fail, and a snapshot that still hits a busy error retries a few times before giving up.
