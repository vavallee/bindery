### Fixed
- **Updating a webhook with a partial body no longer wipes it.** `PUT /api/v1/notification/{id}` now changes only the fields you send. Before, `{"enabled":false}` blanked the name, URL and headers and cleared every event flag. A request that would leave the name or URL empty is now rejected with 400, and a changed URL is checked against the outbound URL policy.
