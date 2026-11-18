### Fixed
- **Handed off files with odd characters in their names no longer reappear in the folder scan.** If a file you handed to an external import tool had a name that isn't valid UTF-8, like an old Latin-1 name from an archive, Bindery recorded it slightly wrong and the scan offered it again, which could drop a second copy. The exact name is now kept.
