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

- a saved Hardcover API token in `Settings -> General`
- the **Enhanced Hardcover series** toggle enabled in `Settings -> General`

If either requirement is missing, Bindery hides the enhanced controls and the enhanced API endpoints return `404`. Existing local series data keeps working. Operators can still disable the feature for the whole deployment with `BINDERY_ENHANCED_HARDCOVER_API=false`.

## Series Workflow

1. Open **Series**.
2. Create a series manually, or open an existing series populated from your library metadata.
3. Use **Add Book** to attach existing Bindery books and set their series positions.
4. Use **Search** to find a matching Hardcover series.
5. Pick the correct Hardcover result, or remove a bad existing link.
6. Open the linked series to load the Hardcover catalog diff.
7. Use **add** on a single missing row, or **add all** / **Fill gaps** to queue every missing catalog entry.

Added Hardcover books are created as wanted and monitored rows. Bindery then queues indexer searches the same way it does for other wanted books.

## Automatic Links

When you click **Search** on an unlinked series, Bindery first attempts an automatic match. It only auto-links when the top Hardcover candidate is confident, not ambiguous, and has local evidence:

- matching local book titles or catalog overlap, or
- author agreement with books already in the local series

If that evidence is missing, Bindery shows candidates for manual selection instead of linking automatically.

## Missing-Book Fill

Missing-book fill uses the linked Hardcover catalog as the source of truth for missing entries. Bindery skips catalog books that already exist locally as excluded titles, so excluded books are not silently re-added.

The fill action may create new authors and books from Hardcover metadata when the catalog entry is not already in Bindery. Those books are linked back to the series with the catalog position.

## Known Behavior

- Hardcover-backed controls require outbound HTTPS access to Hardcover.
- The fill action can also contact configured indexers because it queues searches immediately.
- A linked series can still have local-only or uncertain entries when local metadata does not cleanly match the Hardcover catalog.
- Removing a Hardcover link does not delete the local series or local books.
- Deleting a local series does not delete linked books from your library.

## Troubleshooting

- Enhanced controls are missing: save a Hardcover API token, then enable **Enhanced Hardcover series** in `Settings -> General`. If your operator disabled the deployment-wide feature flag, they must remove `BINDERY_ENHANCED_HARDCOVER_API=false` and restart Bindery.
- Hardcover test fails: verify the token at [Hardcover API settings](https://hardcover.app/account/api) and make sure Bindery can reach `hardcover.app`.
- Search finds the wrong series: choose a different result manually, or remove the link and search again with a more specific local series name.
- Fill gaps adds nothing: check whether the missing books already exist locally, are excluded, or whether the linked Hardcover catalog has no missing entries relative to the local series.
- Searches are not queued after fill: verify your indexers and download-client search flow are working for normal wanted books.

## See Also

- [`docs/DEPLOYMENT.md`](./DEPLOYMENT.md#enhanced-hardcover-series-data-deployment-note)
- [`CHANGELOG.md`](../CHANGELOG.md)
