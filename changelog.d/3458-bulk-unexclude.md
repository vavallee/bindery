### Fixed
- **Bulk Un-exclude on the author page** (#3458). The Excluded view of an author page had a bulk Exclude and no way back. It now shows Un-exclude for the selected books, backed by a new `unexclude` action on `POST /api/v1/book/bulk`.
