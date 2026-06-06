### Fixed
- **Authors and Books pages now reach past the first 100 entries** (#1010) — the
  list, search, sort, and filters are applied server-side and paginated, so
  libraries with more than 100 authors or books are fully browsable. Previously
  only the first page loaded, author/book name search was limited to that page,
  and the footer always read "1–100 of 100". Author/book name search is now
  matched on the server (book search also matches the author's name).
