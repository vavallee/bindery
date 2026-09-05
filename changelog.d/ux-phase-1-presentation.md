### Fixed
- **The app no longer flashes light before turning dark** — the theme class was applied from a React effect, which runs after the browser has already painted, so every route showed its light background for a frame. It is now set before the first paint.
- **A mistyped or shared URL says so instead of showing an empty page** — any path the app did not recognise rendered the header and nav around nothing at all. `/settings/indexers` also works now; it redirects to the tab it names.

### Changed
- **The Authors and Books filter rows are two menus instead of two rows of pills** — every option is still there, and an applied filter shows both as a count on the Filters button and as a chip beside it that clears it. In table view the sort menu is gone, because the column headers already sort.
- **Refresh and Delete on an author moved into the row's ⋯ menu**, leaving the monitored toggle where it was. The Discover card's own menu now uses the same component, so it gets the keyboard handling it never had.
- **The setup checklist is a single strip** naming the next step rather than a box listing all five, and it stops showing once only one step is left.
- **The Authors rating column is hidden when nothing on the page has a rating** — only OpenLibrary supplies author ratings, so for other libraries it was a column of dashes. It still appears, and still sorts, where there is data.
