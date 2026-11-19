### Fixed
- **ABS import tests no longer reach the network.** About twenty tests asked the live audnex service for audiobook details, which took two minutes per lookup on CI runners with blocked egress and pushed the race job past its timeout. The package now refuses outside connections in tests, so it runs in about 20 seconds under the race detector.
