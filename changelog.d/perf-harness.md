### Added

- A performance harness for contributors. `make bench` runs every Go benchmark ten times and writes a file `benchstat` can compare. `make perf-quick` and `make perf-full` seed a library of 1,000 to 40,000 books, boot the real binary against fake indexers and a fake qBittorrent, and report latency percentiles, throughput, peak memory and CPU and heap profiles for startup, the read endpoints, UI polling during a library scan, indexer search, library scan and import. Nothing changes in the released binary. See `docs/performance-testing.md`.
