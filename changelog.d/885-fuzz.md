### Security
- **Hardened parsers against malformed input with Go fuzz tests** (#885) — added bounded native fuzz targets for the Calibre rollback-metadata decoder and the NZB/torrent indexer fetch-URL SSRF guards, asserting they never panic and never let a loopback, link-local, cloud-metadata, or non-http(s) target through.
