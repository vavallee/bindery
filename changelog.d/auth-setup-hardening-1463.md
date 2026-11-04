### Fixed
- **Harden first run setup** (#1463) - the setup endpoint now requires a JSON request body, and is refused when local accounts are disabled and an SSO provider is configured, so the first SSO login owns the admin role.
