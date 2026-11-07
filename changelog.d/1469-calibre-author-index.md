### Fixed
- **Calibre import no longer rescans every author for each book** (#1469). The import used to load the whole authors table once per book, which made big libraries crawl. It now loads authors once per run and keeps that list up to date as new authors are created, with the same matching rules as before.
