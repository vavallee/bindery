### Fixed
- **Import Mode UI no longer claims Move is the default** (#1444) — the
  selector pre-selected Move while the backend has defaulted to auto
  (hardlink on the same filesystem, else copy) since the seeding fix. Auto is
  now a first-class, selectable mode shown as the default, restoring a UI
  path back to the safe behaviour, and `import.mode` is validated so a typo
  fails loudly instead of silently behaving as auto. Contributed by
  @johnistheman.
