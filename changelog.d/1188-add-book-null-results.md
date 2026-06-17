### Fixed
- **Add Book modal no longer crashes when a search returns no results** (#1188) — an empty metadata book search now serializes as `[]` instead of `null` from `/api/v1/search/book`, and the modal defensively coerces a null body to an empty list, so an empty search shows "No results found." instead of throwing `Cannot read properties of null (reading 'map')`.
