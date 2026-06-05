# Changelog fragments

To avoid every PR editing the same `CHANGELOG.md` (which causes constant merge
conflicts), each change drops a small **fragment** here instead. A maintainer
assembles the fragments into `CHANGELOG.md` at release time, then clears this
directory.

## How to add one

Create a file named `<pr-or-issue-number>-<slug>.md` (or just a short slug if you
don't have a number yet), e.g. `997-drop-folder.md`:

```markdown
### Added
- **Post-import drop folder** (#941) — short, user-facing description of the change.
```

Rules:
- Start with the Keep-a-Changelog section the entry belongs to: `### Added`,
  `### Changed`, `### Fixed`, `### Removed`, `### Security`, or `### Deprecated`.
- Write it the way it should read in the release notes (user-facing, not "fixed a
  bug in scanner.go"). Reference the issue/PR number.
- One fragment per PR. Don't touch `CHANGELOG.md` in your PR.

## Preview / assemble

```bash
make changelog        # prints every fragment, grouped as you wrote them
```

At release, the maintainer merges the fragments into the new version section of
`CHANGELOG.md` and deletes the `*.md` files here (this `README.md` stays).
