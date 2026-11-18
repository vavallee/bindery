### Fixed
- **CI egress allowlist gaps** (#3852). Dependency Review can reach api.deps.dev again, and the Container Scan, DAST and Helm Policy Tests jobs can pull Docker Hub layers that are now served from Docker's S3 bucket, so those checks stop failing on unrelated pull requests.
