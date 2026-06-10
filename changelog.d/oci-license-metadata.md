### Changed
- **Docker image now carries OCI metadata** — the published image declares its license (`org.opencontainers.image.licenses=MIT`), source, title, and description, so registries and `docker inspect` surface them. The web package manifest also declares `MIT`, matching the repo's `LICENSE`.
