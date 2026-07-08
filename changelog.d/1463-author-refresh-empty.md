### Fixed
- **Author metadata refresh no longer wipes enriched fields** (#1463) — the nightly refresh kept the existing description, ratings and rating count when the upstream record came back sparse, instead of overwriting them with empty values.
