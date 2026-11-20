### Fixed
- Starting a library scan is now admin only, matching the scan status and the rest of the library maintenance routes. The scan walks the whole library, so with logins enforced a non admin user no longer gets to trigger it. The Settings scan button and the adoption screen were already admin only, and installs without logins, plus API key requests, behave as before.
