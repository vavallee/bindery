### Fixed
- **Error 203 guidance no longer points at a setting that can't be changed** (#1424) — the NZB grab error and the Troubleshooting wiki suggested disabling Prowlarr's per-indexer Redirect setting, which Prowlarr requires for Usenet indexers. Both now explain the real situation: application-whitelisting indexers (like NZBFinder) have to approve Bindery's identity, tracked in #1425.
