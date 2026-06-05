## Summary



## Checklist

- [ ] Tests added or updated
- [ ] `docs/DEPLOYMENT.md` updated if env vars, config, or upgrade path changed
- [ ] Added a changelog fragment under `changelog.d/` (not an edit to `CHANGELOG.md`) — see [changelog.d/README.md](../changelog.d/README.md)
- [ ] Wiki pages updated if user-facing behaviour changed

## Test plan

- [ ] `make check` (or the individual `go test ./cmd/... ./internal/...` and `cd web && npm run build` steps)

<!-- Only lint, validate (Go), and Security Summary are required to merge.
     Other security scans are advisory; a red Container Scan is often a base-image
     CVE unrelated to your change — mention it and a maintainer will confirm. -->
