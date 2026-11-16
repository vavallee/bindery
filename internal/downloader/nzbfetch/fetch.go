package nzbfetch

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

// MaxBytes caps how much of a successful NZB response is read. It matches the
// cap on .torrent fetches (torrentfetch.DefaultMaxBytes) so an indexer that
// answers with something huge by mistake cannot exhaust memory.
const MaxBytes int64 = 50 << 20

// Fetch downloads the NZB at nzbURL for SABnzbd and NZBGet, which both fetch
// it inside Bindery and hand the bytes to the download client (#3795).
//
// client is the one NewHTTPClient returns, built with the same validate, so
// the redirect hops and the dial are checked too. validate is the SSRF check
// on nzbURL itself; nil applies the download-fetch outbound policy. Callers
// wrap Fetch in Retry: it does not retry by itself.
//
// A non-200 answer becomes Error's diagnosis and a 200 that is not an NZB
// becomes ValidateNZB's. Every other error is scrubbed of URL secrets, since
// it ends up on the download row, in history and in webhook payloads.
func Fetch(ctx context.Context, client *http.Client, validate func(string) error, nzbURL string) ([]byte, error) {
	if validate == nil {
		validate = func(raw string) error {
			return httpsec.ValidateOutboundURL(raw, httpsec.DownloadFetchPolicy())
		}
	}
	if err := validate(nzbURL); err != nil {
		return nil, fmt.Errorf("fetch nzb: %w", httpsec.RedactURLError(err))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nzbURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch nzb: %w", httpsec.RedactURLError(err))
	}
	// Some indexers (e.g. nzbfinder.ws, #1053) fingerprint the User-Agent and
	// serve an anti-bot 403 to Go's default UA, so use the project UA the
	// search path already relies on.
	req.Header.Set("User-Agent", useragent.Get())
	resp, err := client.Do(req)
	if err != nil {
		// Scrub the indexer apikey the *url.Error would otherwise leak into
		// the download row / history / webhook payloads.
		return nil, fmt.Errorf("fetch nzb from indexer: %w", httpsec.RedactURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, MaxErrorBody))
		return nil, Error(nzbURL, resp, body)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes))
	if err != nil {
		return nil, fmt.Errorf("fetch nzb from indexer: %w", err)
	}
	// A 200 is not proof the indexer sent an NZB: a refused, expired or
	// rate-limited grab often comes back as an error page with a success
	// status. Forwarding that to the download client makes the client reject
	// it, and the grab then fails under the client's name (#2105).
	if err := ValidateNZB(nzbURL, resp, body); err != nil {
		return nil, err
	}
	return body, nil
}
