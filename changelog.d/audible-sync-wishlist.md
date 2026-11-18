### Added
- **Audible wishlist import list, through Audible Sync (preview).** Connect a self-hosted [Audible Sync](https://github.com/AlexStansfield/audible-sync) instance in the new **Settings, Audible Sync** tab ([guide](docs/AudibleSync-Wiki.md)), then add an Audible wishlist under **Settings, Import**. Each wishlist book is added as a wanted audiobook and searched for, linked to the work your metadata provider knows when its ASIN matches one. Books the Audible account already owns are skipped by default, since Audible Sync downloads them, and a list can take books off the wishlist once Bindery has imported them. Lists sync every 6 hours (`audiblesync.sync_interval`) or on **Sync now**. Off by default.

### Fixed
- Books and authors with an `audible:` or `audnex:` id are no longer classified as OpenLibrary, so Bindery stops asking OpenLibrary for an id it cannot know.
