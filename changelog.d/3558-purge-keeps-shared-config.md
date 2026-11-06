### Fixed
- **Purging a user no longer deletes shared profiles and root folders** (#3558). Quality profiles, metadata profiles and root folders are instance wide, but they carry the stamp of the admin who created them, so purging that admin wiped them for everyone. They now survive and just lose the stamp.
