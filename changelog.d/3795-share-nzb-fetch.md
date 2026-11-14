### Changed
- **SABnzbd and NZBGet now share one NZB download** (#3838, #3795). The two clients each carried a copy of the code that fetches an NZB from an indexer. It now lives in one place, so a fix to the address checks, size limit or error handling applies to both. Behaviour is unchanged. Thanks NopeCharon.
