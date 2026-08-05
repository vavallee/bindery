### Fixed
- **Authors created via Hardcover list-sync now get the same default metadata profile as every other author-create path** (#1736) — previously they landed with no profile assigned at all instead of falling back to the default "Standard" profile. Existing affected authors are backfilled by migration.
