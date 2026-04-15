# Calibre reader test fixtures

`metadata.db` here is a minimal real Calibre-format SQLite database built
by `buildFixtureLibrary` in `reader_test.go`. The helper runs the schema
and seed SQL from this directory at test startup so the on-disk fixture is
always in sync with the Go assertions — you don't need to regenerate it
manually, and deleting the file is harmless.

The per-book directories (`Author/Book (id)/`) hold stand-ins for the
format files referenced in `data` rows so the reader's absolute-path
resolution can be exercised end-to-end.
