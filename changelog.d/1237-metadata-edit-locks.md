### Added
- **Manual metadata editing with field locks** (#1237, #1446) — the book page
  gains an Edit action for title, description, genres, language, and release
  date. Every field you edit is locked so the nightly metadata refresh,
  author-works sync, and ABS/Calibre re-imports keep your values (a 🔒 marks
  locked fields; one click unlocks them all). Genres can also be applied in
  bulk: an author-level override in Edit Author and a Set genre action per
  series stamp + lock the same genre list across all their books, so an
  opinionated genre taxonomy in folder paths (`{Genre}/...`) stays clean.
  Re-bind and metadata re-map clear locks — an explicit identity change asks
  for the new record wholesale.
