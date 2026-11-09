### Changed
- **Telemetry counts Unraid installs**. The daily ping (when telemetry is on) now reports `unraid` as the deployment method for containers started by Unraid, detected from the `HOST_OS=Unraid` variable Unraid sets on every container. Before, those showed up as plain `docker`. Setting `BINDERY_DEPLOY_METHOD` still overrides it.
