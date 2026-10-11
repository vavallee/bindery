### Security
- **Bumped the Go toolchain to 1.26.9**, fixing [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617), an HTTP/2 server crash from an HPACK encoder race in `net/http`. Every build target (`go.mod`, the three Dockerfiles, every CI workflow) moves together, the same discipline #2956 established for the release binaries and the container image.
