### Fixed
- **Adding or removing an OIDC provider no longer wipes the others**. Settings sent every saved provider it had not added in the same visit back with a blank issuer, client ID and scopes and no allowed groups, so adding or removing one provider broke all the others until they were set up again. It now sends each one back exactly as saved, and Bindery refuses a save that would clear a saved provider's issuer or client ID. If a provider stopped working after such a change, edit it and fill those in again.

### Added
- **Edit a saved OIDC provider in place**. Each provider in Settings has an Edit button that opens its form filled in, including a new Allowed groups field (one group per line). Leave the client secret blank to keep the saved one. Before this the only way to change a provider was to remove it and add it again, which also dropped its allowed groups.
