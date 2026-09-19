# Enhanced Hardcover Series

Bindery can use a saved Hardcover API token to improve the Series page with token-backed series search, manual and automatic Hardcover links, catalog diffs, and missing-book fill.

This page is the user-facing companion to the deployment notes in [`docs/DEPLOYMENT.md`](./DEPLOYMENT.md#enhanced-hardcover-series-data-deployment-note).

## What It Does

When enhanced Hardcover series data is enabled, Bindery can:

- create, rename, monitor, and delete local series from the Series page
- add existing Bindery books to a local series with a position number
- search Hardcover for the matching catalog series
- link or unlink a local series to a Hardcover series
- compare the local series against the Hardcover catalog
- show present, missing, local-only, and uncertain catalog entries
- add one missing Hardcover book, or add all missing books for a linked series
- create wanted and monitored book rows for missing catalog entries and queue searches for them

Local series management remains available without the enhanced Hardcover feature. Hardcover-backed search, linking, catalog diffs, and catalog-based missing-book fill are hidden until all enablement requirements are met.

## Before You Start

Enhanced Hardcover series data requires:

- a saved Hardcover API token in `Settings -> API Keys`
- the **Enhanced Hardcover series** toggle enabled in `Settings -> API Keys`

If either requirement is missing, Bindery hides the enhanced controls and the enhanced API endpoints return `404`. Existing local series data keeps working. Operators can still disable the feature for the whole deployment with `BINDERY_ENHANCED_HARDCOVER_API=false`.

## Series Workflow

Use **Search series...** at the top of the Series page to filter your local series by title instead of using browser Find in a large collection. Matching ignores case and accents; clear the field to show all series again. This local search is available without enhanced Hardcover data.

1. Open **Library → Series**.
2. Create a series manually, or open an existing series populated from your library metadata.
3. Use **Add Book** to attach existing Bindery books and set their series positions.
4. Use **Search** to find a matching Hardcover series.
5. Pick the correct Hardcover result, or remove a bad existing link. Each candidate carries a **View on Hardcover** link so you can open it before choosing, which matters most for light novels, where the novel, its manga adaptation and a spin-off all come back under near-identical names.
6. Open the linked series to load the Hardcover catalog diff. The linked series shows the same **View on Hardcover** link, so there is always a way back to the record it is bound to.
7. Optionally use **Set genre** before adding books. The override is applied to current and newly added books in the series and locked against metadata refresh.
8. Use **add** on a single missing row, or **add all** / **Fill gaps** to queue every missing catalog entry.

Added Hardcover books are created as wanted and monitored rows. Bindery then queues indexer searches the same way it does for other wanted books, unless **Auto-grab** is switched off in `Settings -> General`. With auto-grab off, fill still creates the books and marks them wanted and monitored, and queues no searches at all: grab them by hand from the Wanted page when you are ready.

## Where Series Come From

Most series rows are not created on the Series page at all. They arrive when Bindery syncs an author's catalog: each work the metadata providers return carries its series membership, and Bindery upserts that series and links the book to it as the book is created.

When Hardcover enriches an author whose works come from OpenLibrary, both providers can claim a series for the same book. Hardcover wins. Its series carry a stable catalog id and a position, while OpenLibrary parses a free text string, and keeping both would create two local series for one real one and put the book in each. Providers other than Hardcover only fill in a series when the work has none yet.

Refreshing an author links the series of the books you already have, not only the ones the refresh creates. That is how an imported library gains its series: point the author at the metadata record you want and refresh, and each book the provider puts in a series is filed under it. A link that is already stored is left exactly as it is, position included, so a position you corrected by hand survives every later refresh. Nothing is unlinked, and a book already in a series keeps that series even when the provider now names the same one under a different catalog id. Matching those two up ignores case, spacing, apostrophes and a parenthetical suffix, but not an inverted article, so a stored "Expanse, The" and an incoming "The Expanse" still end up as two series.

Two more limits are worth knowing:

- A series that came from Hardcover metadata is linked to the Hardcover catalog as it is created, because the provider supplied the catalog id exactly. Series from other providers are local series like any other, and linking them is the manual step described below.
- Series created before this behaviour existed keep no link. Use **Search** on the series to link them, which also unlocks the catalog diff and missing-book fill for them.

## Automatic Links

When you click **Search** on an unlinked series, Bindery first attempts an automatic match. It only auto-links when the top Hardcover candidate is confident, not ambiguous, and has local evidence:

- matching local book titles or catalog overlap, or
- author agreement with books already in the local series

If that evidence is missing, Bindery shows candidates for manual selection instead of linking automatically.

## How the Catalog Diff Binds Local Books

The diff pairs each local book in the series with at most one catalog entry, in two passes. A local book whose provider ID matches a catalog entry binds to it first, whatever the library order. Only then are the remaining local books matched by title, and a title match is never allowed across positions: a book the series files at position 4 cannot bind to catalog volume 9 however similar the titles are. Title matches are made in order of each book's best score, ties in library order. A book whose best entry is already taken moves on to its next best, unless a book with the same title holds an entry it matches better than that next best. The same title means the same numbers, and two titles at least as close to each other as the book is to that entry. The two rows are then copies of one book (a duplicate import, or an ebook and an audiobook row) and the second is listed as Local only, with or without stored positions: a second copy of The Way of Kings does not claim The Way of Kings Prime, which stays in Missing. That is logged at DEBUG as `series diff: local book is a second copy of a book already bound, left Local only`. A book whose best entry went to a different book, such as "He Who Fights with Monsters 4" losing the bare series title to volume 1, is not a copy and moves on to its own entry.

Title matching is still a similarity score, and it has known limits. Books that score the same against one entry take it in library order, so the pairing can change with that order: with catalog titles like "The Primal Hunter 1" and local titles like "The Primal Hunter 13: A LitRPG Adventure", volume 13 can take catalog volume 1, and a second copy of volume 1 then takes catalog volume 13. For the same reason a book that moves on can land on an entry it only resembles when nothing closer is free. Stored positions on both sides limit those two to entries that share a position, because a title match never crosses positions. A second copy whose title differs from the first is not recognised as a copy: "The Way of Kings: Book One of the Stormlight Archive" beside "The Way of Kings", with both rows and The Way of Kings Prime filed at position 1, still takes Prime.

Every binding decision is logged at DEBUG as `series diff: local book bound to catalogue entry` with the local and catalog IDs, positions and whether it was matched by identity or title, so a wrong pairing can be read straight out of the log.

## Missing-Book Fill

Missing-book fill uses the linked Hardcover catalog as the source of truth for missing entries. Bindery skips catalog books that already exist locally as excluded titles, so excluded books are not silently re-added. That skip applies to the excluded title itself, not to other books whose titles it happens to contain, so an excluded box set does not stand in the way of the volume it is named after.

Catalog entries whose titles name a box set rather than a book ("box set", "boxed set", "collection set", "3 Books Set") are never created by fill, the same way they are pruned from an author catalogue on ingestion. They are still listed in the diff, so clicking **add** on one does nothing and reports nothing queued.

The fill action may create new authors and books from Hardcover metadata when the catalog entry is not already in Bindery. Those books are linked back to the series with the catalog position.

The format dropdown beside **add all** sets the media type of every book the fill creates. Pick **Ebook** and the created books are ebook only, even when Hardcover lists an audiobook edition of the same work, so only one search is queued per book. Pick **Both** if you want Bindery to look for both formats. Books that are already in the series keep whatever media type they were added with, so change those on the book itself.

## Known Behavior

- Hardcover-backed controls require outbound HTTPS access to Hardcover.
- The fill action can also contact configured indexers because it queues searches immediately. Switching **Auto-grab** off in `Settings -> General` stops that, for fill as well as for the scheduled sweep.
- A linked series can still have local-only or uncertain entries when local metadata does not cleanly match the Hardcover catalog.
- **View on Hardcover** is built from the series slug, which is the only identifier hardcover.app routes series pages on. Series linked before Bindery started recording the slug have none stored, so their link appears the next time the catalog diff is loaded. If Hardcover reports a series with no slug at all, Bindery shows no link rather than one that leads to a missing page.
- Removing a Hardcover link does not delete the local series or local books.
- Deleting a local series does not delete linked books from your library.

## Troubleshooting

- Enhanced controls are missing: save a Hardcover API token, then enable **Enhanced Hardcover series** in `Settings -> API Keys`. If your operator disabled the deployment-wide feature flag, they must remove `BINDERY_ENHANCED_HARDCOVER_API=false` and restart Bindery.
- Hardcover test fails: verify the token at [Hardcover API settings](https://hardcover.app/account/api) and make sure Bindery can reach `hardcover.app`. Both `hc_pat_` personal access tokens and the older JWTs are accepted. The test tells you which side failed: `token rejected (HTTP 401: ...)` is a token to reissue, while `HTTP 500 (upstream returned a non-JSON response ...)` is a Hardcover outage to wait out. See [Troubleshooting](./Troubleshooting-Wiki.md#hardcover-fails-with-http-500-or-the-token-test-shows-a-hardcover-error-page).
- Search finds the wrong series: choose a different result manually, or remove the link and search again with a more specific local series name.
- Fill gaps adds nothing: check whether the missing books already exist locally, are excluded, or whether the linked Hardcover catalog has no missing entries relative to the local series.
- Searches are not queued after fill: check the **Auto-grab** toggle in `Settings -> General` first, since it suppresses fill's searches deliberately, then verify your indexers and download-client search flow are working for normal wanted books.

## See Also

- [`docs/DEPLOYMENT.md`](./DEPLOYMENT.md#enhanced-hardcover-series-data-deployment-note)
- [`CHANGELOG.md`](../CHANGELOG.md)
