### Fixed
- **"Wanted" filter no longer shows unmonitored books** (#1173) — on the author page, the "Status: Wanted" filter now lists only genuinely wanted books (monitored and not yet imported). An unmonitored book carrying a stale `wanted` status is excluded, matching the status badge and the backend's wanted filter.

### Added
- **Select all / deselect all on the author page** (#1172) — the author page filter bar gains a single toggle that selects or deselects every currently displayed book for bulk actions. It respects the active filters, so it composes with the Wanted filter and only ever sweeps up visible books.
