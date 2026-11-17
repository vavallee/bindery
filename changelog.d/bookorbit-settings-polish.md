### Fixed
- **BookOrbit library names after a reload** (#1927). The ticked library and the import history showed "Library 1" until you pressed Load libraries. Bindery now looks the names up on its own when the page opens, and only falls back to "Library 1" when BookOrbit cannot be reached.
- **Stale BookOrbit messages on the settings page**. An old error from Load libraries no longer hangs around after you test the connection, import or save. Each action clears the last one's result.
- **Plain BookOrbit connection errors** (#1927). A wrong host, a refused connection, a timeout, a certificate problem, http and https mixed up, or an unexpected status now gets a short explanation instead of the raw Go error. The full detail stays in the server log.
- **BookOrbit API key access label**. The setup help and docs now say "Read only", which is what BookOrbit calls that option.
