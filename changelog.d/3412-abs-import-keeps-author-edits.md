### Fixed
- **Audiobookshelf import no longer undoes author edits made while it runs** (#3407): when an import linked an author you had picked by hand, it wrote back the copy of that author it read at the start, so a bio or photo saved in the meantime was lost. It now re-reads the author and only writes if nothing changed since.
