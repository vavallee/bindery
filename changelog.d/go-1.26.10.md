### Security
- **Built with Go 1.26.10.** The binaries and the container image move from Go 1.26.9 to 1.26.10, which fixes a standard library vulnerability in `crypto/x509` (GO-2026-6809) that `govulncheck` reports as reachable from Bindery's code.
