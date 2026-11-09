### Fixed
- **Grimmory connections now use the same network guard as Audiobookshelf** (#1472). Redirects and lookups that land on link-local or cloud metadata addresses are refused, and the standard `HTTP_PROXY` and `NO_PROXY` variables apply. If you set `HTTP_PROXY`, add your Grimmory host to `NO_PROXY` so a LAN Grimmory keeps connecting directly.
- **A bad scheduler interval is logged instead of silently skipped** (#1472). The log names the job that could not be scheduled.
