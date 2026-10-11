### Added
- **Books / Authors filter in Add to Library** (#3126). The result list has an All / Books / Authors control. The Add Book button opens on a flat list of books in relevance order and the Add Author button on authors only, so a title search no longer sits under a stack of author rows. Switching views is instant, and each view shows how many results it holds.

### Changed
- **Author results show a work count more often** (#3126). The count beside an author in Add to Library now also comes from Hardcover, and is kept when another provider's record for the same author wins the merge. Previously it only appeared when OpenLibrary or NB supplied the row.
