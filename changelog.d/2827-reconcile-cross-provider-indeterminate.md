### Fixed
- **Catalogue reconciliation keeps unmatched previous-provider books** (#2827): Switching metadata providers no longer makes uncorrelated metadata-only Wanted rows look safe to remove. Complete same-provider absence and explicit profile rejections remain actionable. Thanks to @kevinatlee for the fix.
