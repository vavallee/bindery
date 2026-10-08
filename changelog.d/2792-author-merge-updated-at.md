### Fixed

- **Merging two authors no longer writes a malformed `updated_at` on the moved books** (#2792). `AuthorAliasRepo.Merge` bound a raw `time.Time` into the `books` re-parenting update, so the SQLite driver stored Go's `time.String()` layout (`2006-01-02 15:04:05.999999999 +0000 UTC`) instead of the canonical RFC3339 shape every other `books` writer uses. Reads tolerated the odd layout, but anything comparing or ordering `updated_at` as text saw two shapes on the merged rows. Merge now binds `timeValueArg(time.Now().UTC())`, matching the rest of the time-column convention (#914).
