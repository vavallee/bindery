### Fixed
- **Log search takes wildcards literally** (#1466). Searching the logs for a term containing `%`, `_`, or `\` now matches those characters as text instead of treating them as SQL wildcards, so you get the results you expect.
