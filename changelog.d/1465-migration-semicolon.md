### Fixed
- **Migration runner semicolon gotcha** (#1465) — the SQL migration runner
  split statements on `;` before stripping comments, so a semicolon inside a
  `--` comment (or a string literal) corrupted the statement list and aborted
  boot. The splitter is now aware of line comments, block comments, and
  quoted literals, so migration authors can write natural SQL comments.
