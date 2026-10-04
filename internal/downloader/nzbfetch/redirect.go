package nzbfetch

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
)

// maxRedirects matches net/http's default, so a chain that worked before this
// policy existed still works.
const maxRedirects = 10

// crossHostHeaders are the only request headers carried onto a redirect hop
// whose host differs from the original request's. Anything else the caller
// set is for the host it was addressed to.
var crossHostHeaders = []string{"User-Agent", "Accept"}

// NewHTTPClient returns the client SABnzbd and NZBGet use to fetch an NZB from
// an indexer. The transport re-validates every dialled address against the
// download-fetch SSRF policy, and RedirectPolicy covers the redirect hops,
// which matters most when an outbound proxy is configured and the transport
// dials the proxy rather than the target.
//
// validate is the same check the caller applies to the first URL; it is
// called again for every hop.
func NewHTTPClient(validate func(string) error) *http.Client {
	return &http.Client{
		Timeout:       60 * time.Second,
		Transport:     httpsec.GuardedTransport(httpsec.DownloadFetchPolicy()),
		CheckRedirect: RedirectPolicy(validate),
	}
}

// RedirectPolicy is the CheckRedirect for NZB fetches. Following the redirect
// is required: Prowlarr answers every Usenet download with a 302 to the
// indexer (see Error). What Go does on that hop by default is the problem:
//
//   - It sets Referer to the previous URL, query string included, so the
//     Prowlarr apikey in the release link was sent to the third party
//     indexer on every grab. Referer is removed on every hop.
//   - It copies the original request's headers to the new host, dropping
//     only Authorization and Cookie. On a cross-host hop only
//     crossHostHeaders survive.
//   - It does not re-check the new URL. Every hop goes through validate, the
//     same SSRF check the first URL passed.
func RedirectPolicy(validate func(string) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if validate != nil {
			if err := validate(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
		}
		req.Header.Del("Referer")
		if len(via) > 0 && !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
			for name := range req.Header {
				if !slices.Contains(crossHostHeaders, http.CanonicalHeaderKey(name)) {
					req.Header.Del(name)
				}
			}
		}
		return nil
	}
}
