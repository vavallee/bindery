### Changed

- **The unmatched-files hint now tells you what actually failed** (#1958) — when
  files are found but nothing matches, Bindery used to give one answer: populate
  the author's book catalogue and refresh the author. That is right for exactly
  one of the four ways a file can miss. The scan now records a reason per file
  and shows it in the Unmatched files table, and when the parsed author matches
  no author in your library at all the hint names that author and points at the
  file's tags and folder name instead of sending you to refresh an author who
  was never the problem. A file whose name and tags yield no title at all now
  says so instead of claiming no book matched a title that was
  never there.
