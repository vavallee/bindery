### Fixed
- **Login failures are throttled per address and per account and address pair** (#TBD). Wrong passwords are counted for each address and account pair and for each address overall. Account names ignore case and surrounding spaces, and IPv6 addresses in the same /64 share a count. The same limits apply to OPDS Basic auth.
