### Fixed
- **Wrong-author grabs from embedded title phrases** (#1539) — the release
  relevance filter accepted any release whose name contained the book's title
  words as a contiguous phrase, with no author check on that path, so a
  different work embedding the requested title could be grabbed and imported
  ("Reborn as an Assassin's Apprentice, Vol. 1 by okiuta" matched for Robin
  Hobb's "Assassin's Apprentice"; reported by cleb on Discord with the root
  cause pinned). Phrase and in-order title hits are now only trusted on their
  own when *anchored* — preceded by nothing but the author, a series index
  ("Book 1", "Vol. 2"), numbers, or filler words. When real foreign words sit
  in front of the title (usually another work's longer title), the requested
  author must appear somewhere in the release name. Releases titled with just
  the book title still pass, so the fix costs no recall on the common
  author-less naming shapes; the narrow tradeoff is that a release naming only
  a series *name* (no author, no "Book N" marker) before the title is now
  rejected rather than risk importing the wrong book.
