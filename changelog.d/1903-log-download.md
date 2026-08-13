### Added
- **Download logs from the UI** (#1903) — a **Download** button next to *Clear filters* in `Settings → Logs` saves the entries matching the current filters as a plain-text file (`bindery-logs-<timestamp>.txt`) you can attach to a bug report, no container shell needed. The file records which filters produced it, one entry per line; API keys and tokens in logged URLs are redacted, and an export stops at 50,000 entries and says so. Admin-only, like the rest of the Logs tab.

### Fixed
- **Log date-range filters were silently ignored** (#1903) — the From/To pickers in `Settings → Logs` sent a zone-less timestamp the API couldn't parse, so narrowing the range did nothing. They now send a real instant in your local time zone.
