### Fixed
- **Updating a metadata profile with a partial body no longer resets it.** `PUT /api/v1/metadataprofile/{id}` now changes only the fields you send. Before, `{"name":"x"}` cleared the allowed languages, zeroed the minimum and skip settings and turned an unknown language behavior of fail back into pass. An empty name is now rejected with 400.
