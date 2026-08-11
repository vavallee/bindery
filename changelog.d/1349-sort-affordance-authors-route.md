### Fixed
- **Sortable column headers on the Books page now look sortable** — the Books
  table has supported sorting by author, type and status since v1.28.0
  ([#1349](https://github.com/vavallee/bindery/issues/1349)), but nothing said
  so: the Sort toolbar above the table offers only title and date, the headers
  had no pointer cursor, and the ▲/▼ marker appeared only on the column already
  in use. An unsorted Type or Status header was indistinguishable from plain
  text, so the feature read as missing. Headers now show a pointer cursor, a
  muted ↕ when inactive, and a "Sort by …" tooltip.
- **`/authors` now resolves** — Authors is served from `/` because it was the
  first page that existed, while every nav entry added later (`/books`,
  `/import`, `/settings`, …) got a real path. `/authors` matched no route, and
  with no catch-all it rendered the site chrome around an empty page rather
  than a 404. It now redirects to `/`, the same way `/blocklist` redirects into
  Settings.
