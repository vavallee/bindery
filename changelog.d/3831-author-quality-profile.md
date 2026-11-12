### Fixed
- **Authors with no quality profile no longer hide it** (#3831). An author with no quality profile accepts every format, at grab and at import, so a PDF can land under what looks like an epub only setup. Authors added from the Add Author dialog never got a profile, and Edit Author showed the first profile as if it were picked. Both dialogs now have a "None (accepts every format)" choice that shows the real state, and Add Author (and request approval) let you pick the profile. Thanks to sladester for the detailed report.

### Added
- **Default quality profile for new authors** (#3831). Settings, Quality Profiles has a new default that every newly added author gets when nothing else picks one: the Add Author dialog, adding a book, series fill, the ABS, Calibre and CSV imports, and Hardcover lists without their own profile. It starts empty, which means none.
- **Fix existing authors in one go** (#3831). Please open Settings, Quality Profiles after updating. If some of your authors have no quality profile, a notice says how many and lets you give them one with a confirm step. Only authors with no profile change, and nothing happens until you press Apply.
