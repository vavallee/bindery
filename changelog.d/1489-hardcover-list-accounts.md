### Fixed
- **Per-account Hardcover reading lists** (#1489) — Hardcover's built-in
  shelves ("Want to Read", …) share one slug per account, so loading a second
  person's lists with their token showed their shelf as the already-added
  first one and toggled the wrong list. List identity is now (slug, account):
  the picker reports which Hardcover account it's browsing, saved lists
  remember the account they came from (shown as an @username chip), and two
  households' "Want to Read" lists sync side by side, each with its own
  token.
