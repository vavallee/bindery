### Fixed
- **Adding a book you already have says so** — Add Book used to reuse your existing copy silently and flip it back to monitored. It now tells you the book is already in your library and links to it, and search results for books and authors you already have are marked "In your library" with an Open link instead of Select. To change the format of a book you own, use the book page.

### Security
- **Add Book is scoped to your own library under multi user tenancy** — adding a book another user already had could return that user's copy and change it. The request is now refused without touching or revealing the other copy.
