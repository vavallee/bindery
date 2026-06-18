### Fixed
- **Spurious logout on a database blip** — a transient database error while checking a session's epoch no longer silently invalidates a valid session cookie. The auth middleware now distinguishes a real "session revoked" epoch mismatch from a failed lookup and returns a server error (500) on the latter, instead of dropping the request to unauthenticated and logging the user out.
