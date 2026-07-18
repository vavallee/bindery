### Fixed
- **OIDC callback URLs behind trusted proxies** — redirect auto-detection now retains the original trusted proxy identity after real-client-IP resolution, preventing HTTPS callbacks from incorrectly falling back to HTTP.
