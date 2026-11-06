### Fixed
- **Calibre import keeps books that differ by a subtitle apart** (#3631). Two Calibre books by the same author whose titles differ only by a subtitle used to collapse into one row, and one of them lost its Calibre id. A row that already belongs to another Calibre book is no longer taken over, so both stay in the library.
