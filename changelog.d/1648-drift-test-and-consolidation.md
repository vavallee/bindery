### Fixed
- **Accented titles no longer produce two different indexer searches** (#1648) — the same title could be sent to indexers in two different encodings depending on where it came from, which also gave it two different deduplication keys. Found by the new consistency test, not by a bug report.
- **Books stored with a format tag in the title stop getting a duplicate row** (#1648) — a book saved as "Title [Unabridged]" was not recognised as the same work as OpenLibrary's "Title", so an author sync created a second row for it.
- **Series named "... Series" or tagged "[Audiobook]" can be upgraded to full Hardcover data** (#1648) — the lookup and the upgrade check disagreed about what counts as the same series name, so some series matched one and failed the other.

### Changed
- **Author name handling is defined in one place** (#1648) — several helpers that build author search queries and sort names existed as identical copies in four packages. They now live in one, and a new test suite compares every normalization rule in the codebase against a corpus of awkward names and titles (dotted initials, possessives, umlauts, Nordic and Polish letters, CJK, Cyrillic, Greek, Hebrew) so that two of them disagreeing is a build failure rather than a bug report months later.
