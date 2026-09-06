### Changed
- **Settings now refuse a key Bindery does not recognise** (#2311). Saving a setting through the API used to accept any key at all, so a typo such as `serch.interval` saved, reported success and then did nothing forever with no way to tell it apart from a setting that was working. The write is now rejected with the key named. Reading and deleting are unchanged, so a row left behind by another build is still listed and can still be removed.

### Added
- **A description of every setting, over the API** (#2311). `GET /api/v1/settings/descriptors` returns each key Bindery knows about with its type, its default, the values it accepts, a one line explanation, whether a change needs a restart, and whether anything reads it at all. That last part matters: two keys are still stored purely for compatibility and are read by nothing, so a client can now say so instead of offering a control that does nothing.
