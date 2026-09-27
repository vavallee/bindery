## Summary

<!-- What changed and why, in a few sentences. -->

Closes #

## How it was verified

<!-- For a fix: show the new test failing on main for the reason in the issue,
     then passing here. A test that passes before the fix proves nothing.
     For a feature: what you ran, and against what data. -->

## Checklist

- [ ] Every commit carries a `Signed-off-by` that matches its author (`git commit -s`), see [Sign your work](../CONTRIBUTING.md#sign-your-work-dco). Commits authored by a coding agent fail this check; author them as yourself.
- [ ] Changelog fragment added as `changelog.d/<issue-number>-<slug>.md`, not an edit to `CHANGELOG.md`, see [changelog.d/README.md](../changelog.d/README.md)
- [ ] Tests added or updated
- [ ] `docs/DEPLOYMENT.md` updated if env vars, config, or upgrade path changed
- [ ] Wiki pages under `docs/` updated if user-facing behaviour changed
- [ ] No new dependency, or its licence is permissive (Bindery is MIT; GPL, AGPL, SSPL and BUSL are not accepted) and `THIRD_PARTY_LICENSES.md` is regenerated

## Test plan

- [ ] `make check` (or the individual `go test ./cmd/... ./internal/...` and `cd web && npm run build` steps)

<!-- Only lint, validate (Go), and Security Summary are required to merge.
     Other security scans are advisory; a red Container Scan is often a base-image
     CVE unrelated to your change. Mention it and a maintainer will confirm. -->
