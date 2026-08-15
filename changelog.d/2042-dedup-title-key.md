### Fixed
- **Duplicate books from punctuation and subtitle disagreements** (#2042) — the
  canonical title key now folds apostrophes (`Poseidon's Arrow` and `Poseidons
  Arrow` are one book) and treats a colon as a separator rather than a
  truncation point (`Journey of the Pharaohs: Numa Files #17` matches the
  colon-less spelling), so a provider that punctuates differently from Calibre
  no longer creates a second `wanted` row beside a book you already own.

### Changed
- **Books that share a main title are no longer merged** (#2042) — the key used
  to stop at the first `": "`, so `Star Wars: A New Hope` and `Star Wars: The
  Empire Strikes Back` were one identity and an import could bind one onto the
  other. Distinct subtitles now mean distinct books; a subtitle only one source
  spells out still matches. Keys are recomputed automatically on the next start,
  so no action is needed; libraries already holding duplicates keep them, as
  merging existing rows remains a separate piece of work.
