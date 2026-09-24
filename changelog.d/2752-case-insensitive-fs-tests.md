### Fixed
- **`make test` on a case insensitive filesystem** (#2752): two download path tests assumed the filesystem could tell `books` and `Books` apart, so the suite failed for contributors on macOS. They now probe the filesystem they are running on and assert what it should do there. Test only, no change to how Bindery validates paths. Thanks magrhino for the report.
