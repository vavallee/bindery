### Fixed
- **Faster library, book, and search queries** — the query that finds each book's ebook/audiobook file ran twice per book listing and was doing a full scan of the `book_files` table because no index covered its `format` filter. A new composite index makes it an index seek, which is most noticeable on large libraries and on the paginated Books page. Applied automatically on upgrade.
