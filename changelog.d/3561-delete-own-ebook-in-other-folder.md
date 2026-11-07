### Fixed
- **Deleting a book or an author with its files removes an ebook that sits inside another book's folder** (#3561). The file was left on disk, untracked, while the delete reported success. The other book's own files stay protected.
