### Fixed
- **ABS import no longer stalls on an author with a huge catalogue** (#2578): matching an imported book against the author's works also fetched a cover for every one of those works, which for an author like Arthur Conan Doyle meant thousands of rate limited requests inside a single item, with nothing in the log. The title match now uses the plain works list. Each item also has a ten minute limit: an item that runs past it is marked failed and skipped, and a restart moves on instead of resuming into it again. Thanks MarcoCSE for the detailed report.

### Changed
- **ABS import says why files were not attached**: when an item imports as metadata only, the log now has one line for it with the reason, the library roots it was compared against and the path remap in use. A remapped ABS mount only gets files when it is one of Bindery's roots; the ABS import guide explains the two ways to set that up.
