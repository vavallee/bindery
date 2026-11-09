// Package torrentfetch downloads a .torrent file from an indexer link on behalf
// of the torrent download clients (qBittorrent, Deluge, Transmission, rTorrent).
//
// Every one of them fetches the file inside Bindery and hands the bytes to the
// client, so the request runs in Bindery's DNS namespace rather than the
// client's (often a VPN container) and the client never needs to reach the
// indexer. The four clients used to carry their own copy of this loop and the
// copies had drifted: different timeouts, Transmission quietly truncated an
// oversized body and accepted an empty one. This is the single copy (#1470).
// The NZB side is separate, see package nzbfetch.
package torrentfetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	// Timeout bounds one whole fetch, every redirect hop included: Fetch puts
	// it on the context, not only on each request. A .torrent is small but
	// indexers behind a proxy can be slow to produce it, so this is
	// deliberately longer than the 15 second RPC timeout Deluge and
	// qBittorrent used to inherit by accident.
	Timeout = 60 * time.Second

	// MaxRedirects is how many redirect hops are followed before giving up.
	MaxRedirects = 5

	// DefaultMaxBytes caps the response body when Options.MaxBytes is unset.
	// It matches the cap on NZB fetches so an indexer that answers with an
	// HTML page by mistake cannot exhaust memory.
	DefaultMaxBytes int64 = 50 << 20

	// errBodyBytes is how much of a failed response body is echoed into the
	// error, enough to read an indexer's reason without buffering junk.
	errBodyBytes = 256
)

// Options configures one Fetch.
type Options struct {
	// Transport performs the requests. Production callers pass the
	// download-fetch guarded transport (httpsec.GuardedTransport with
	// httpsec.DownloadFetchPolicy) so a DNS rebind between Validate and the
	// dial cannot reach a forbidden host. A nil Transport uses net/http's
	// default, which only tests that talk to a loopback server should want.
	Transport http.RoundTripper

	// Validate is the SSRF check applied to the first URL and again to every
	// redirect target before it is requested. Nil applies the download-fetch
	// outbound policy.
	Validate func(string) error

	// MaxBytes caps the response body; zero means DefaultMaxBytes. A longer
	// body is an error, never a truncated torrent.
	MaxBytes int64

	// Timeout overrides the package Timeout; zero means Timeout. Only tests
	// need it.
	Timeout time.Duration
}

// Result is either the .torrent bytes or, when the link redirected to a
// magnet: URI, that magnet (#1006). Exactly one field is set.
type Result struct {
	Data      []byte
	MagnetURL string
}

// Fetch resolves an indexer http(s) download link.
//
// Redirects are followed by hand so that a hop to a magnet: Location is
// captured instead of being handed to net/http, which rejects the scheme. Each
// hop is validated, restricted to http and https, and the Referer is never
// sent, so an indexer apikey in a link is not forwarded to the next host. The
// request carries the project User-Agent because some indexers serve an
// anti-bot 403 to Go's default (#1053). Errors are scrubbed of URL secrets.
func Fetch(ctx context.Context, rawURL string, opt Options) (*Result, error) {
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	maxBytes := opt.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	validate := opt.Validate
	if validate == nil {
		validate = func(raw string) error {
			return httpsec.ValidateOutboundURL(raw, httpsec.DownloadFetchPolicy())
		}
	}
	// http.Client.Timeout restarts on every hop, so the deadline lives on the
	// context to bound the whole chain.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{
		Transport: opt.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	current := rawURL
	for redirects := 0; redirects <= MaxRedirects; redirects++ {
		if err := validate(current); err != nil {
			return nil, fmt.Errorf("fetch torrent: %w", httpsec.RedactURLError(err))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch torrent: build torrent fetch request: %w", httpsec.RedactURLError(err))
		}
		req.Header.Set("Accept", "application/x-bittorrent")
		req.Header.Set("User-Agent", useragent.Get())

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch torrent: request to indexer failed: %w", httpsec.RedactURLError(err))
		}

		if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
			location := resp.Header.Get("Location")
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if location == "" {
				return nil, fmt.Errorf("fetch torrent: redirect without location")
			}
			if magnet := strings.TrimSpace(location); strings.HasPrefix(strings.ToLower(magnet), "magnet:") {
				// A magnet is one line. qBittorrent reads its urls field one
				// link per line, so a CR, LF or NUL would smuggle a second
				// link in; no client has a use for one, so refuse it here.
				if strings.ContainsAny(magnet, "\r\n\x00") {
					return nil, fmt.Errorf("fetch torrent: redirect magnet link contains a line break or NUL")
				}
				return &Result{MagnetURL: magnet}, nil
			}
			next, err := req.URL.Parse(location)
			if err != nil {
				return nil, fmt.Errorf("fetch torrent: invalid redirect location: %w", httpsec.RedactURLError(err))
			}
			if next.Scheme != "http" && next.Scheme != "https" {
				return nil, fmt.Errorf("fetch torrent: unsupported redirect scheme %q", next.Scheme)
			}
			current = next.String()
			continue
		}

		data, err := readBody(resp, maxBytes)
		if err != nil {
			return nil, err
		}
		return &Result{Data: data}, nil
	}
	return nil, fmt.Errorf("fetch torrent: too many redirects")
}

// readBody reads and closes a final (non redirect) response.
func readBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyBytes))
		return nil, fmt.Errorf("fetch torrent: indexer returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch torrent: %w", httpsec.RedactURLError(err))
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("fetch torrent: response exceeds %d bytes", maxBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("fetch torrent: empty torrent response")
	}
	return data, nil
}
