### Added
- **Download logs from the UI** (#1903) — a **Download** button next to *Clear filters* in `Settings → Logs` saves the entries matching the current filters as a plain-text file (`bindery-logs-<timestamp>.txt`) you can attach to a bug report, no container shell needed. The file records which filters were actually applied, one entry per line; API keys, tokens and line breaks are stripped from every part of the line, and an export stops at 50,000 entries and says so. On installs with no persistent log store the file names the filters it could not apply instead of implying it honoured them. Admin-only, like the rest of the Logs tab.

### Fixed
- **Log date-range filters were silently ignored** (#1903) — the From/To pickers in `Settings → Logs` sent a zone-less timestamp the API couldn't parse, so narrowing the range did nothing. They now send a real instant in your local time zone.
- **A To-only log date range produced a file that didn't match the table** (#1903) — setting only *To* left the on-screen table unbounded below while the download covered just the hour before *To*. The export now defaults its range on the same condition the table does.
