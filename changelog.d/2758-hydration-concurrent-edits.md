### Fixed
- **Hardcover refresh keeps concurrent book edits** (#2758): a book edit made while Hardcover editions are being fetched, including a language detected from an imported EPUB, is no longer overwritten by the refresh.
- **Search after a Hardcover refresh uses current book state** (#2758): monitoring and ASIN values are reloaded before searching, even when the edition fetch is empty, unchanged, or fails.
