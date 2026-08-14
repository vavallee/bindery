### Fixed

- **A dual-format folder now attaches both files in one library scan** (#1957) —
  a scan claimed a book the moment any one file matched it, so a folder holding
  `Title.epub` and `Title.m4b` attached one and left the other in Unmatched
  until you ran a second full scan. Claims are now per format, which is how
  book files are stored anyway. Two files of the *same* format still can't both
  claim one book. An ebook sitting next to an already-attached audiobook is no
  longer skipped as "already tracked" either — that shortcut now only absorbs
  the audiobook's own sibling tracks, plus the supplement files an audiobook
  release ships (a companion PDF, liner notes, a stray `.txt`), which are never
  attached as the book's ebook edition. A PDF in a folder with no audio in it is
  still an ebook.
