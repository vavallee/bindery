### Added
- **Fix a book's series from the book** (#2554). Edit metadata has a Series field, limited to the author's series, with a position and New series… to create one, so a book filed under the wrong series or missing its number can be fixed without going through the series page. No series takes the book out of every series it is in. Admin only.
- **Removed stays removed** (#2554). A book taken out of a series is no longer put back by the next refresh. The book page lists the series it was taken out of, with Restore, and Unlock all fields hands the book's series back to refresh.

### Fixed
- **One primary series per book** (#2525). Adding a book to a series as primary now makes it the book's only primary series.
- **Position is optional** when adding a book to a series, as it already was on the server.
- **Series controls for other users.** Users who aren't admins no longer see series buttons that the server refuses (add, rename, merge, delete, fill, Hardcover link, a book's series in Edit metadata). They still see every series and what is missing.
- **Series grouping after a refresh.** The author page regroups its books by series after a refresh, without a reload.
