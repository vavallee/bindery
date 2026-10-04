### Security
- **Tracker passkeys in the URL path are now hidden too** (#2958). Some private trackers put the passkey or RSS key in the download link's path instead of a parameter, so it still showed up in search, queue, pending and history results and in the log export. Those path keys are now redacted the same way, and grabs still send the real link.
- **Usernames and passwords written into a feed or download link are hidden** (#2958). A link like `https://user:pass@host/...` no longer shows its credentials in results, history or the log export.
