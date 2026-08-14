### Fixed

- **Audiobooks tagged with a contributor list now match on library scan** (#1956)
  — an m4b whose Artist tag names the author plus their translator and narrator
  ("Álvaro Enrigue, Natasha Wimmer - translator, Gabriel Porras") could never
  reconcile: the tag replaced the author your `Author/Title/` folders had
  already resolved correctly, and no author in your library matches a whole
  contributor list, so the file returned to Unmatched on every scan. The tag is
  still preferred when it matches, but it no longer destroys the folder author
  — when the tag matches nobody, the scan falls back to the folder author and
  then to the credited names in the list — every one of them that matches an
  author you have, so a book catalogued under the second name is still found.
