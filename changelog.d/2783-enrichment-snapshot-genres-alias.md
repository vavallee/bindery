### Fixed
- **Genres read from the shared metadata cache are no longer shared with the book that reads them** (#2783) — the enrichment cache stored a book's `Genres` list by reference and handed that same list to every later book for the work, so editing one of those lists in place would have changed the cached copy too. The cache now keeps and returns its own copy.
