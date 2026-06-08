### Security
- **Bump the release image's Go toolchain to 1.26.4** — the published Docker image was built with Go 1.26.3, which has known standard-library vulnerabilities (incl. the high-severity CVE-2026-42504) that the container scan flagged. The build now uses the patched Go 1.26.4 release. CI's `go.mod`/test matrix already tracked the patched 1.25.11 line; this aligns the shipped binary.
