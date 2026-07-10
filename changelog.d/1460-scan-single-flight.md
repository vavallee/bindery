### Fixed
- **Library scan single-flight** (#1460) — triggering a manual library scan while another scan is running (manual or the scheduled one) now returns 409 Conflict instead of starting a second concurrent walk that could race on book creation and clobber the last-scan status.
