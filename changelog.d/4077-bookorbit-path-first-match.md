### Fixed

- **BookOrbit import no longer creates duplicate books when the file is already tracked** (#4077). When the file an item reports is already on a Bindery book under a different spelling of the author, the item is parked in the review queue instead of creating a new author and a new wanted book that could never take the file.
- **Failed import items are now listed on the run** (#4077). A failed item is recorded in the import run's entities with its message, so after a run there is a list of what failed instead of only the last 100 status results. Thanks to gavinmcfall for the report and to Huyvux12 for the fix.
