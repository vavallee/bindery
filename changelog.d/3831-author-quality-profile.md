### Fixed
- **Authors with no quality profile no longer hide it** (#3831). An author with no quality profile accepts every format, at grab and at import, so a PDF can land under what looks like an epub only setup. Authors added from the Add Author dialog never got a profile, and Edit Author showed the first profile as if it were picked. Both dialogs now have a "None (accepts every format)" choice that shows the real state, and Add Author (and request approval) let you pick the profile. Thanks to sladester for the detailed report.

### Changed
- **API clients:** `PUT /api/v1/author/{id}` now treats an explicit `"qualityProfileId": null` as clearing the author's quality profile. Leaving the field out keeps the profile it has (#3831).

### Added
- **Default quality profile for new authors** (#3831). Settings, Quality Profiles has a new default that every newly added author gets when nothing else picks one: adding a book, series fill, the ABS, Calibre and CSV imports, Hardcover lists without their own profile, and the Add Author and request approval dialogs when you leave their picker alone. It starts empty, which means none. If the default profile has upgrades on, authors created by an ABS or Calibre import join the upgrade sweep straight away.
- **Fix existing authors in one go** (#3831). Please open Settings, Quality Profiles after updating. If some authors have no quality profile, a notice says how many and lets you give them one with a confirm step. It covers every author in the library with no profile, including ones added by approving a request. Only authors with no profile change, and nothing happens until you press Apply.
