### Fixed
- **Bulk "search" on the Wanted page is much faster** — selecting a large batch and hitting search issued one of the heaviest book queries per book instead of a single batched fetch, so a 500-book bulk search ran hundreds of full-table aggregations. It now loads all selected books in one query.
