### Security
- **Tracker passkeys in the URL path are now hidden too** (#2958). Some private trackers put the passkey or RSS key in the download link's path instead of a parameter, so it still showed up in search, queue, pending and history results and in the log export. Those path keys are now redacted the same way, and grabs still send the real link.
