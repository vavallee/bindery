### Added

- **OIDC client secrets can come from the environment** (#4159). Set `BINDERY_OIDC_<ID>_CLIENT_SECRET` (for example `BINDERY_OIDC_KEYCLOAK_CLIENT_SECRET` for a provider with id `keycloak`) and Bindery uses it instead of the stored secret. It is never written to the database or its backups, and the provider form shows the field as set via environment variable with the variable name. Works with Docker and Kubernetes secrets and other secret injectors. Thanks to GianniBYoung for the request.
