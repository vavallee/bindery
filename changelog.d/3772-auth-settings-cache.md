### Changed
- **Fewer database reads per request** (#1468). The auth mode, API key and session secrets are now cached in memory and refreshed the moment you change them, so each request does fewer SQLite reads. Password changes and sign outs still take effect on the very next request.
