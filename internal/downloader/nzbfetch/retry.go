package nzbfetch

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

// Attempts is how many times an NZB fetch is tried before the grab is failed.
//
// Deliberately small. On the Prowlarr redirect path Bindery fetches straight
// from the indexer's own file host, and many indexers count every getnzb hit
// against a daily grab allowance — so a retry can cost a grab, not merely a
// request. Three covers the observed flake, where the same release alternated
// failure and success minutes apart, without letting one wanted book make a
// meaningful dent in a day's quota.
const Attempts = 3

// backoffFor is the pause before the attempt following a failure: 500ms, then
// 1s. A var so tests can assert behaviour instead of wall-clock.
var backoffFor = func(attempt int) time.Duration {
	return 500 * time.Millisecond * (1 << attempt)
}

// Retry runs fetch until it succeeds, fails in a way another attempt cannot
// fix, or runs out of attempts.
//
// Only the fetch is retried, never the upload to the download client: a
// repeated addfile risks a duplicate job, whereas a repeated GET costs at most
// a request. Without this a single transient failure fails the grab
// permanently — the download row goes to failed, the book drops back to
// Wanted, and nothing tries again until the next scheduled search, which makes
// a momentary blip indistinguishable from "no release found" (#2157).
func Retry(ctx context.Context, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < Attempts; attempt++ {
		if attempt > 0 {
			if err := pause(ctx, backoffFor(attempt-1)); err != nil {
				return nil, lastErr
			}
		}
		body, err := fetch(ctx)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !transient(ctx, err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// transient reports whether another attempt could plausibly clear err.
//
// The test is deliberately positive: only failures recognisable as a network
// or timing fault retry, and everything else is left alone. Defaulting the
// other way would turn every permanent misconfiguration — a bad URL, a
// blocked-by-policy host, an indexer refusing the grab — into three requests
// and three times the log noise for the same outcome.
//
// An indexer that refuses the download says so just as firmly on the third ask
// as the first, and hammering a rate-limited indexer is how the limit was
// reached. Those failures arrive from Error and ValidateNZB as plain messages
// with no network cause underneath, so they fall through to false on their own
// rather than needing to be enumerated here.
func transient(ctx context.Context, err error) bool {
	// A caller that has given up is not an indexer flake, and retrying would
	// keep working for a request whose result nobody is waiting for any more.
	if ctx.Err() != nil {
		return false
	}
	// The fetch client's own deadline firing, or a body that stopped short
	// part-way through — both of which cleared on a later attempt in practice.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var nerr net.Error
	return errors.As(err, &nerr)
}

// pause waits d, or returns early if the caller gives up first. A retry must
// not keep a cancelled request alive for the length of its own backoff.
func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
