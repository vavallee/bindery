### Changed
- **Imports stay inside the download folder** (#4083). A book listed by the download client is now skipped when it sits behind a symlinked folder that resolves outside the download, and file copies refuse a source that is a symlink or not a regular file. Normal nested folders, and downloads you reach through your own link, import as before.
