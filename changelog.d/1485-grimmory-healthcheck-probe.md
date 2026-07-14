### Fixed
- **Grimmory "Test connection" against Grimmory v3.x** (#1485) — the connection
  test probed `GET /api/status`, a route current Grimmory (v3.x) no longer has.
  Its Spring security layer answers any unmapped `/api/**` path with a 401
  Whitelabel page, which looked like an auth wall (and was mistaken for one in
  #1448) but was really a missing route, so the test could never pass and failed
  with `invalid character '<'`. The probe now hits Grimmory's public
  `GET /api/v1/healthcheck` endpoint for reachability and version; credential
  verification stays with the separate login round-trip, so "Test connection"
  still reports whether your username/password actually work.
