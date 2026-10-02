### Added
- **Per indexer seed time and inactive seed time** (#2206): a torrent indexer can now carry a total seeding time and an inactive seeding time, in minutes, next to its seed ratio, and both are sent to the download client when a release is grabbed. qBittorrent honors both (the inactive limit needs qBittorrent 4.6 or later), Transmission honors the inactive limit as its idle limit, and Deluge and rTorrent have no per torrent time limits so they keep their own rules. Indexers synced from Prowlarr pick up Prowlarr's per indexer seed time until you edit them. Thanks ThatDeltaGuy for the request.

### Fixed
- **Per indexer seed ratio now reaches Transmission** (#2206): the ratio was sent with the add request, which Transmission ignores, so torrents kept the global ratio. It is now applied right after the torrent is added.
