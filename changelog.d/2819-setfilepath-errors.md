### Fixed
- **Existing library files found when adding an author are recorded properly** (#2819): if Bindery could not load the book while recording a file it already found in your library, it used to mark the book imported without registering the file, and reported success even when the book did not exist. It now logs the real error instead. Thanks to @magrhino for the report.
