### Fixed
- **Transmission polls fetch the torrent list once** (#3714). With an audiobook category set, the import poll and the stall check pulled the whole torrent list from Transmission once per category, and the import poll sometimes a third time. They now fetch it once and filter locally with the same matching rule, which halves the work on big sessions.
