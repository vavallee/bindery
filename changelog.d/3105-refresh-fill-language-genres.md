### Fixed
- Refreshing an author now also fills a language and genres that an existing book is missing, from the provider record that book already came from (#3105). A value already there, a locked field and a book from another provider are left alone, and a refresh never clears anything.
