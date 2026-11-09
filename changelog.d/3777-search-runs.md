### Added
- **Search run ids** (#3133). Searches started from the bulk actions, a book update, add with search and request approval now hand back a run id, and `GET /api/v1/search/runs/{id}` tells you whether that search is still going or how it ended. Thanks kennethp1015 for the request.
