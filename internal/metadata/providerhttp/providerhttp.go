// Package providerhttp is the request loop the HTTP metadata providers share:
// OpenLibrary, Google Books, DNB, NB, Audible and Audnex. It retries a
// refusal (HTTP 429), a gateway or availability failure (502, 503, 504) and a
// transport error, honours Retry-After, backs off with jitter when the server
// gives no hint, drains error bodies so the connection is reused, and marks
// what is left after the retries with the providererr sentinels so callers
// can tell "the provider would not answer" from "this record has no answer".
//
// It exists because only OpenLibrary had any of this (#2075, #2101), and the
// other clients were what OpenLibrary's loop used to look like: a 429 was a
// hard failure that the caller logged as "metadata lookup failed" and moved
// past (#2369). Every future retry fix now lands in one place.
//
// The second half is the Gate. A refusal holds every request to that provider,
// not only the one that was refused: a bulk import has several catalogue
// fetches and edition samples in flight against the same provider at once,
// and each of them backing off on its own schedule meant the rest kept
// sending while the provider was asking for quiet (#2075). Hardcover keeps its
// own adaptive pacer (internal/metadata/hardcover/throttle.go) because its
// GraphQL transport, body caps and per account tiers need more than this, but
// it marks its refusals with the same providererr sentinels.
package providerhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/useragent"
)

// MaxRetries bounds how many times a retryable failure is retried before Do
// gives up, so a call makes at most MaxRetries+1 requests.
const MaxRetries = 3

// BaseDelay and MaxDelay bound the exponential backoff used when a refusal
// carries no Retry-After header.
const (
	BaseDelay = 500 * time.Millisecond
	MaxDelay  = 8 * time.Second
)

// RetryAfterCap bounds how long a single Retry-After value is honoured for.
// Providers are expected to send small values; capping defensively means a
// malformed or hostile header cannot bench a provider for the rest of a run.
const RetryAfterCap = 30 * time.Second

// errorBodyBytes is how much of an error response is kept for the message.
const errorBodyBytes = 512

// maxDrainBytes bounds how much of an error response is read and discarded so
// the transport can return the connection to its pool. Closing after a
// partial read forces a fresh TCP connection on the next retry, which is the
// "connection refused" escalation #2075 reported under load.
const maxDrainBytes = 1 << 20

// StatusError is a non 200 answer that was not handed back to the caller. A
// 429 matches providererr.ErrRateLimited and a 5xx matches
// providererr.ErrUnavailable through errors.Is.
type StatusError struct {
	Code int
	// Body is the first part of the response body, with secrets redacted.
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

// Is reports the provider state this status stands for.
func (e *StatusError) Is(target error) bool {
	switch target {
	case providererr.ErrRateLimited:
		return e.Code == http.StatusTooManyRequests
	case providererr.ErrUnavailable:
		return e.Code >= http.StatusInternalServerError
	}
	return false
}

// HeldError reports that the provider had asked for quiet until a time past
// the caller's deadline, so the request was not sent at all. It matches
// providererr.ErrRateLimited: the caller learns the provider was refusing
// rather than that it was slow.
type HeldError struct {
	Provider string
	Until    time.Time
}

func (e *HeldError) Error() string {
	name := e.Provider
	if name == "" {
		name = "metadata provider"
	}
	return fmt.Sprintf("%s asked for requests to wait until %s, past this request's deadline", name, e.Until.UTC().Format(time.RFC3339))
}

// Is matches providererr.ErrRateLimited.
func (e *HeldError) Is(target error) bool { return target == providererr.ErrRateLimited }

// Gate holds every request to one provider while that provider is refusing.
// One Gate belongs to one client and is shared by every request it makes. The
// zero value is ready to use, and a nil *Gate gives each call a private one,
// so a retry still waits but other calls do not.
type Gate struct {
	mu    sync.Mutex
	until time.Time
}

// NewGate returns an open gate.
func NewGate() *Gate { return &Gate{} }

// hold keeps the gate shut for d from now, unless it is already shut longer.
func (g *Gate) hold(d time.Duration) {
	if d <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := time.Now().Add(d); until.After(g.until) {
		g.until = until
	}
}

// heldUntil returns the time the gate opens; the zero time means open.
func (g *Gate) heldUntil() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.until
}

// wait blocks until the gate opens. It refuses up front, with a *HeldError,
// when the gate opens after ctx's deadline: sleeping into a timeout would turn
// "the provider asked us to wait" into "the provider is down". It loops
// because another request may extend the hold while this one sleeps.
func (g *Gate) wait(ctx context.Context, provider string) error {
	for {
		until := g.heldUntil()
		delay := time.Until(until)
		if delay <= 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if deadline, ok := ctx.Deadline(); ok && until.After(deadline) {
			return &HeldError{Provider: provider, Until: until}
		}
		if err := sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// Request describes one provider request.
type Request struct {
	// Provider names the provider in transport error messages, as in
	// "openlibrary request: ...". Empty leaves transport errors unprefixed.
	Provider string
	// Method defaults to GET.
	Method string
	URL    string
	// Header is added to every attempt. A User-Agent is always set.
	Header http.Header
	// Pass lists statuses besides 200 that are handed back to the caller
	// rather than turned into a *StatusError, such as a 404 a client maps to
	// "not found".
	Pass []int
	// Redact flattens transport errors into their redacted text. Set it when
	// the URL carries a credential: a *url.Error's message embeds the full
	// URL, so the key would otherwise reach any log line that prints the
	// error (#1144). It costs the error chain, so keyless providers leave it
	// off and keep errors.Is(err, context.Canceled) working.
	Redact bool
}

// Do sends r through gate, retrying as the package doc describes. It returns
// the response when the status is 200 or listed in r.Pass, and the caller
// closes its body. Every other outcome is an error: a *StatusError for a
// status, a *HeldError when the provider's hold outlasts ctx, or the
// transport or context error.
func Do(ctx context.Context, client *http.Client, gate *Gate, r Request) (*http.Response, error) {
	if gate == nil {
		gate = &Gate{}
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}

	var lastErr error
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		if err := gate.wait(ctx, r.Provider); err != nil {
			var held *HeldError
			if errors.As(err, &held) {
				// The refusal that set the hold says more than the hold
				// does. Neither carries a URL, so neither needs redacting.
				if lastErr != nil {
					return nil, lastErr
				}
				return nil, err
			}
			return nil, transportErr(r, err)
		}

		req, err := http.NewRequestWithContext(ctx, method, r.URL, nil)
		if err != nil {
			return nil, err
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		req.Header.Set("User-Agent", useragent.Get())

		resp, doErr := client.Do(req)
		if doErr != nil {
			// A cancelled operation is never worth retrying, whatever
			// cancelled it, and neither is one whose caller's own deadline
			// has passed. A transport failure says nothing about the
			// provider asking for quiet, so it backs off this call only.
			if errors.Is(doErr, context.Canceled) || ctx.Err() != nil || attempt == MaxRetries {
				return nil, transportErr(r, doErr)
			}
			lastErr = transportErr(r, doErr)
			if err := sleep(ctx, BackoffDelay(attempt+1, 0)); err != nil {
				return nil, transportErr(r, err)
			}
			continue
		}

		if resp.StatusCode == http.StatusOK || passes(r.Pass, resp.StatusCode) {
			return resp, nil
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyBytes))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
		statusErr := &StatusError{Code: resp.StatusCode, Body: httpsec.RedactSecrets(string(body))}

		if !IsRetryableStatus(resp.StatusCode) {
			return nil, statusErr
		}
		// Hold the whole provider, not just this call: the other requests in
		// flight are what keeps a throttled provider throttled (#2075).
		gate.hold(BackoffDelay(attempt+1, ParseRetryAfter(resp.Header.Get("Retry-After"))))
		if attempt == MaxRetries {
			return nil, statusErr
		}
		lastErr = statusErr
	}
	// Unreachable: the last iteration always returns.
	return nil, lastErr
}

func passes(pass []int, status int) bool {
	for _, s := range pass {
		if s == status {
			return true
		}
	}
	return false
}

func transportErr(r Request, err error) error {
	if r.Redact {
		return errors.New(httpsec.RedactSecrets(err.Error()))
	}
	if r.Provider != "" {
		return fmt.Errorf("%s request: %w", r.Provider, err)
	}
	return err
}

// IsRetryableStatus reports whether status is a transient upstream state worth
// another attempt. Any other 4xx means the request itself is wrong.
func IsRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// ParseRetryAfter reads a Retry-After header, either a delay in seconds or an
// HTTP date, capped at RetryAfterCap. It returns 0 when the header is absent
// or unusable, and the caller computes its own backoff.
func ParseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		if secs > int(RetryAfterCap/time.Second) {
			return RetryAfterCap
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			if d > RetryAfterCap {
				return RetryAfterCap
			}
			return d
		}
	}
	return 0
}

// BackoffDelay picks the wait before retry number attempt (the first retry is
// 1). A positive retryAfter wins outright, since the server said how long.
// Otherwise it doubles from BaseDelay up to MaxDelay with equal jitter, half
// the delay plus a random share of the other half, so requests refused
// together do not all come back together.
func BackoffDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	if attempt < 1 {
		attempt = 1
	}
	d := MaxDelay
	if attempt <= 10 {
		d = BaseDelay << uint(attempt-1) //nolint:gosec // attempt is bounded above, no overflow
	}
	if d <= 0 || d > MaxDelay {
		d = MaxDelay
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1)) //nolint:gosec // backoff jitter, not security sensitive
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
