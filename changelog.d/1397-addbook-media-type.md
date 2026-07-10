### Added
- **Format selector on Add Book** (#1397) — the Add Book modal (Authors/Home
  page) now has an ebook / audiobook / both selector, matching the Series
  page. Default keeps the previous behaviour (provider metadata, falling back
  to the `default.media_type` setting). Picking a format applies it to the
  added book even when it already existed in the library, re-evaluating
  wanted status so the missing format gets searched.
