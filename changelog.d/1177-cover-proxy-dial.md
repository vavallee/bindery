### Fixed
- **Cover images load again when `BINDERY_OUTBOUND_PROXY` is set** (#1177) — the cover-image proxy no longer applies its strict per-dial SSRF re-check to the outbound proxy itself, which had rejected a LAN/loopback proxy address and returned `502` for every cover (silently breaking the on-disk image cache). The DNS-rebind guard still applies on the direct, no-proxy path.
