### Changed
- **`make check` now runs `govulncheck`**, pinned to the same revision CI installs. `CONTRIBUTING.md` described the target as running what the gating CI checks run, and listed the vulnerability scan among them, but the target omitted it. A contributor who ran `make check` before opening a PR had not actually run the scan CI gates on.

### Removed
- **Three scratch files that were committed by accident**: `err.log`, `magazine-feature-prompt.txt` and `ui-browser-test-prompt.txt`. They shipped in every clone. Their siblings were already ignored; these were missed. They stay on disk locally, only the tracking is removed.
