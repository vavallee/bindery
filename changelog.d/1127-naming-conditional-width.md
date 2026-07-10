### Added
- **Conditional text and zero-pad widths in naming templates** (#1127) —
  literal text placed inside a brace group renders only when its token has a
  value (`{Title}{ - Series}` emits the dash only for series books, no more
  trailing separators), and numeric tokens accept a width modifier
  (`{SeriesNumber:2}` → `02`) so alphabetic filename sorts keep parts in
  order. Both are backward compatible: bare tokens, `{Genre:Unsorted}`-style
  defaults, and unknown-token passthrough behave exactly as before. One edge:
  a 1–2 digit modifier is now a width, so a numeric *default* that short is
  no longer supported (3+ digits, e.g. `{Year:2024}`, still works as a
  default).
