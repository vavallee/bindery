### Security
- **System logs are now admin-only** — `/system/logs` and `/system/loglevel` were reachable by any authenticated user, exposing the app-wide log stream (other users' book and author names, OIDC usernames, download titles) and letting a non-admin flip the global log level. They now require admin, matching every other global-infra route.
