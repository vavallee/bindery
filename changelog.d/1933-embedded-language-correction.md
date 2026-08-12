### Fixed
- **A book whose file is in a different language than the catalogue says now
  gets corrected on import**
  ([#1933](https://github.com/vavallee/bindery/issues/1933)) — the language on a
  book page came from the metadata provider, which describes the abstract
  *work*, not the file you actually hold. The embedded `dc:language` tag was
  read only when the provider had supplied nothing at all, so a Spanish EPUB
  imported against an English OpenLibrary record displayed "English"
  indefinitely, with the release name buried in a history row the only hint
  otherwise. The tag is now read on every EPUB import and, when it disagrees
  with the stored value, the file wins: a work has editions in many languages,
  but the file on disk is one specific edition and is the thing you open. The
  correction is recorded as a **Language Corrected** history event showing both
  codes, so "why does my English book read as Spanish" has an answer in the
  place you would look for it rather than only in a log line.

  Precedence is user, then file, then provider. A language you set by hand locks
  the field, and a locked field is left alone — the EPUB is not even opened.
  Filling a language the catalogue never had is unchanged and stays silent: it
  is a gap being filled, not a disagreement, and OpenLibrary routinely supplies
  no work-level language at all. Comparison normalises both sides first, so a
  provider's `en` against a file's `eng` is not mistaken for a conflict, and
  nothing is written when an import fails — a book that never landed must not
  rewrite its own catalogue entry.
