// Package nzbfetch diagnoses failed NZB downloads from indexers. Both usenet
// download clients (SABnzbd, NZBGet) fetch the NZB themselves before handing
// the bytes to the client; when that fetch fails the raw response body is
// often a newznab XML error blob that is unreadable in logs and gives the
// operator no hint about what to change. This package turns the failure into
// a structured, actionable error message (issue #1404).
package nzbfetch

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// MaxErrorBody is how much of a failed response body callers should read and
// pass to Error. Newznab error documents are ~130 bytes; 2 KiB leaves room
// for longer descriptions and HTML error pages without buffering junk.
const MaxErrorBody = 2048

// rawBodyCap bounds how much of a non-newznab body is echoed into the error
// message, matching the 256-byte cap the clients used before this package.
const rawBodyCap = 256

// newznabError is the <error code="…" description="…"/> document newznab
// indexers return on API failures.
type newznabError struct {
	XMLName     xml.Name `xml:"error"`
	Code        string   `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

// Error builds the error for a non-2xx NZB fetch response.
//
// requestedURL is the URL Bindery was asked to fetch (the release's nzbUrl);
// resp is the final response after any redirects — resp.Request.URL reflects
// where the chain actually landed. When the fetch was redirected to a
// different host than requested, the indexer saw Bindery's own identity
// instead of the proxy's (Prowlarr etc.), which is exactly how app-whitelisting
// indexers like NZBFinder come to reject the grab with error 203 even though
// the same release downloads fine from inside Prowlarr (#1404). That hop is
// called out so the failure is explainable, but there is no user-side setting
// that removes it: Prowlarr refuses to disable Redirect for Usenet indexers
// and no longer proxies NZB downloads (#1424), so the only real fix is the
// indexer whitelisting Bindery's identity (#1425).
func Error(requestedURL string, resp *http.Response, body []byte) error {
	msg := describeBody(resp.StatusCode, body)
	if hop := crossHostHop(requestedURL, resp); hop != "" {
		msg += hop
	}
	return fmt.Errorf("fetch nzb: %s", msg)
}

// describeBody renders the response body as a structured newznab error when
// it parses as one, and as a truncated raw snippet otherwise.
func describeBody(status int, body []byte) string {
	var ne newznabError
	if err := xml.Unmarshal(body, &ne); err == nil && ne.Code != "" {
		return fmt.Sprintf("indexer refused the download (HTTP %d, newznab error %s: %s)", status, ne.Code, strings.TrimSpace(ne.Description))
	}
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > rawBodyCap {
		snippet = snippet[:rawBodyCap]
	}
	return fmt.Sprintf("indexer returned HTTP %d: %s", status, snippet)
}

// crossHostHop returns a diagnostic suffix when the final response host
// differs from the requested host, i.e. a redirect handed the fetch off to
// another server. Hostname (not host:port) comparison so an http→https or
// port rewrite on the same machine doesn't read as a hand-off.
func crossHostHop(requestedURL string, resp *http.Response) string {
	req, err := url.Parse(requestedURL)
	if err != nil || resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return ""
	}
	from, to := req.Hostname(), resp.Request.URL.Hostname()
	if from == "" || to == "" || from == to {
		return ""
	}
	return fmt.Sprintf(" — the download request was redirected from %q to %q, so the indexer saw Bindery directly instead of the app that performed the search. Indexers that whitelist applications (NZBFinder is the known case) reject that with error 203. No Prowlarr setting avoids this hop: Prowlarr requires Redirect for Usenet indexers and no longer proxies NZB downloads. The fix is the indexer adding Bindery to its approved applications — see the error 203 entry in the Troubleshooting wiki", from, to)
}
