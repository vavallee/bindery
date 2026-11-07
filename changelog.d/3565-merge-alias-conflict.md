### Fixed
- **Merging authors says when a pen name could not be kept** (#3565). Alias names are unique across the instance, so if another author already holds the merged author's name, the merge used to succeed without recording it and tell you nothing. The merge response now lists those names in `AliasesSkipped` and the server logs a warning.
