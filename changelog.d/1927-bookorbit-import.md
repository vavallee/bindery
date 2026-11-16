### Added
- **BookOrbit import** (#1927). Bindery can now learn what you already own from a [BookOrbit](https://github.com/bookorbit/bookorbit) server. Under Settings, BookOrbit, enter the server URL and a read only API key, tick the libraries to read, and add a path remap if BookOrbit and Bindery mount the files at different paths. A preview shows what an import would do without changing anything. The import then matches or creates each author, book and series and registers every ebook and audiobook file where it already sits. Files Bindery cannot see are counted and left unregistered, and running it again creates nothing twice. It only reads: nothing is written to BookOrbit and no file is moved, renamed or deleted. BookOrbit's API is not promised to be stable, so the whole selection is read and checked before anything is written, and a response the importer does not understand stops the run with nothing imported. Setup and known limits are in [docs/BookOrbit-Import.md](docs/BookOrbit-Import.md). Thanks to gavinmcfall and gerwitz for the request and for the details of their libraries.

### Fixed
- **Import previews counted a new author once per book** (#1927). An Audiobookshelf dry run reported "authors created" as the number of books by authors Bindery did not have yet. It now counts each new author once, which is what the import itself does.

### Changed
- **An Audiobookshelf import and a BookOrbit import cannot run at the same time** (#1927). They share one import engine, so the second is refused with a 409 until the first finishes.
