//go:build canary

// Package canary_test exercises bindery's real metadata clients against the
// LIVE third-party provider APIs (OpenLibrary, Hardcover, Google Books). It
// exists to catch upstream API drift — schema changes, new auth/UA
// requirements, endpoint deprecations — the day it happens instead of via
// user bug reports (the class of failure behind #1048, where Hardcover
// started rejecting the author-works query, and #1053, where an indexer
// began 403-ing our User-Agent).
//
// Design rules:
//   - Build-tagged `canary` so it never runs in normal CI or `go test ./...`.
//     Run it explicitly: go test -tags canary -count=1 -v ./tests/canary/
//   - Assertions are drift-tolerant: "no error, non-empty, core fields
//     parse". Providers reorder and rescore results constantly, so nothing
//     here asserts exact content or ordering.
//   - Rate-limit friendly: a handful of requests per provider per run, once
//     nightly (.github/workflows/canary.yml). The clients under test already
//     send the project User-Agent via internal/useragent.
//   - Authenticated providers skip (not fail) when their credential env var
//     is absent, so the canary degrades gracefully on forks and local runs.
package canary_test

import (
	"context"
	"testing"
	"time"
)

// testCtx returns a context bounded well under the suite's -timeout so a
// hung provider fails the individual test with a deadline error instead of
// panicking the whole run.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}
