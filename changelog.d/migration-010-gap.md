### Fixed
- **bindery-dev (and any install that received a legacy-index binary on a fresh DB) no longer crashloops** — a no-op migration `010_noop.sql` fills the gap in the sequence, so the row legacy runners wrote as version 10 is now a valid filename-based version and the migration guard accepts it without treating the table as corrupt.
