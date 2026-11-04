### Fixed
- **OPDS authors and series feeds no longer fail on a huge page number.** A request such as `/opds/authors?page=9223372036854775807` returned a server error. Every OPDS feed now caps the page number and answers with an empty page.
