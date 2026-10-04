### Changed
- **`:latest` now means the latest release, and nothing else.** The image tag used to move on every merge to `main` as well, so anyone running `:latest` could pick up unreleased code. It now moves only when a release is tagged. If you want the head of `main`, use the new `:edge` tag.
- **The container image is built with the same Go toolchain as the release binaries and CI** (Go 1.26.8). The image had drifted to a newer Go than the one the test suite and vulnerability scan run against.

### Security
- **Release binaries are now signed with build provenance.** Every archive and the checksums file on a GitHub Release carries a SLSA provenance attestation, like the container image already did. Check a download with `gh attestation verify <file> --repo vavallee/bindery`.
