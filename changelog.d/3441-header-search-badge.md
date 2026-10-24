### Changed
- **One header search, and a failed downloads badge** (#3441). The library search offers "Search indexers for ..." and opens the indexer search with the query, also when that page is already open with another query. Activity and Queue show how many downloads failed, refreshed every minute from a new `GET /api/v1/queue/failed-count`. `/activity` opens Wanted.
