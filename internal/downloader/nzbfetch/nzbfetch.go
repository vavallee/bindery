// Package nzbfetch diagnoses failed NZB downloads from indexers. Both usenet
// download clients (SABnzbd, NZBGet) fetch the NZB themselves before handing
// the bytes to the client; when that fetch fails the raw response body is
// often a newznab XML error blob that is unreadable in logs and gives the
// operator no hint about what to change. This package turns the failure into
// a structured, actionable error message (issue #1404).
package nzbfetch

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
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

// utf8BOM is the byte order mark Windows tooling puts in front of an XML
// declaration. It is legal, some indexers emit it, and encoding/xml refuses to
// parse past it.
var utf8BOM = []byte("\xef\xbb\xbf")

// prologScanLimit bounds how much of a body is examined — and how much of a
// compressed body is expanded — to find the root element. An NZB declares
// <nzb> within its first few hundred bytes, so this is generous; the point is
// that a decompression bomb must not turn validation into an allocation
// attack.
const prologScanLimit = 1 << 20

var (
	gzipMagic  = []byte{0x1f, 0x8b}
	bzip2Magic = []byte("BZh")
)

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
	if detail := newznabDetail(status, body); detail != "" {
		return detail
	}
	return fmt.Sprintf("indexer returned HTTP %d: %s", status, snippet(body))
}

// newznabDetail renders body as a structured newznab error, or "" when body is
// not one.
func newznabDetail(status int, body []byte) string {
	var ne newznabError
	if err := xml.Unmarshal(body, &ne); err != nil || ne.Code == "" {
		return ""
	}
	return fmt.Sprintf("indexer refused the download (HTTP %d, newznab error %s: %s)", status, ne.Code, strings.TrimSpace(ne.Description))
}

// snippet renders body as a trimmed, capped excerpt for an error message.
func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > rawBodyCap {
		s = s[:rawBodyCap]
	}
	return s
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

// ValidateNZB reports an error when an otherwise successful NZB fetch returned
// a body that is not an NZB document.
//
// Both usenet clients fetch the NZB themselves and hand the bytes to SABnzbd /
// NZBGet, so a response the indexer got wrong arrives at the download client as
// the download client's problem: SAB answers status:false and the grab fails
// with "SABnzbd rejected download", naming the one component in the chain that
// behaved correctly (#2105). Indexers reach this path by answering a refused,
// expired or rate-limited grab with HTTP 200 and an error page, which never
// touches Error's non-2xx route.
//
// Rejecting here also keeps the bad body out of the download client's spool.
// SAB writes it, fails to parse it, and purges it before its own backup step,
// so the bytes that explain the failure are gone by the time anyone looks.
func ValidateNZB(requestedURL string, resp *http.Response, body []byte) error {
	decoded := decodeBody(body)
	if !decoded.unreadable && rootIsNZB(decoded.plain) {
		return nil
	}
	msg := describeNonNZB(resp.StatusCode, decoded)
	if hop := crossHostHop(requestedURL, resp); hop != "" {
		msg += hop
	}
	return fmt.Errorf("fetch nzb: %s", msg)
}

// decodedBody is a fetched body with any archive wrapper removed.
type decodedBody struct {
	plain      []byte // content to validate, capped at prologScanLimit
	compressed bool   // arrived as a gzip or bzip2 file
	unreadable bool   // claimed to be an archive and could not be decompressed
}

// decodeBody unwraps a body an indexer served as a compressed *file* —
// Content-Type application/x-gzip with no Content-Encoding, which the HTTP
// transport does not unwrap for us because we never asked for gzip ourselves.
//
// Both usenet clients sniff the magic and decompress it themselves; SABnzbd's
// process_single_nzb is explicit about it ("Supports NZB, NZB.BZ2, NZB.GZ and
// GZ.NZB-in-disguise") and keys off the leading bytes rather than the upload
// filename. Validating the wrapper instead of the content would therefore
// reject bodies the download client accepts today, turning working grabs into
// failed ones — the outcome this whole check exists to avoid.
//
// The original compressed bytes are still what gets uploaded. This is only so
// the check can see what is inside them.
func decodeBody(raw []byte) decodedBody {
	r, compressed, err := unwrapArchive(raw)
	if err != nil {
		return decodedBody{compressed: true, unreadable: true}
	}
	plain, err := io.ReadAll(io.LimitReader(r, prologScanLimit))
	if err != nil && compressed {
		return decodedBody{compressed: true, unreadable: true}
	}
	return decodedBody{plain: bytes.TrimPrefix(plain, utf8BOM), compressed: compressed}
}

// unwrapArchive returns a reader over raw's decompressed content, and whether
// raw was compressed at all.
func unwrapArchive(raw []byte) (io.Reader, bool, error) {
	switch {
	case bytes.HasPrefix(raw, gzipMagic):
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		return zr, true, err
	case bytes.HasPrefix(raw, bzip2Magic):
		return bzip2.NewReader(bytes.NewReader(raw)), true, nil
	default:
		return bytes.NewReader(raw), false, nil
	}
}

// rootIsNZB reports whether the body's first XML element is <nzb>. Only the
// root decides: an error page parses far enough to yield a root element, and
// reading past it would mean walking the whole document to learn what the
// first token already settled.
//
// Strict is off and the charset reader passes bytes through so that a sloppy
// but genuine NZB — a non-UTF-8 declaration, an unescaped entity in a subject
// line — is not rejected as junk; a hard rejection here turns a working grab
// into a failed one, which is a worse outcome than the bug being fixed.
// Neither leniency lets a non-NZB through, because the root element name is
// what decides and it is ASCII under every encoding an indexer plausibly uses.
func rootIsNZB(plain []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(plain))
	dec.Strict = false
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return strings.EqualFold(se.Name.Local, "nzb")
		}
	}
}

// describeNonNZB renders a rejected body as a structured newznab error when it
// is one — some indexers serve those with HTTP 200 — and as a capped snippet
// otherwise. An unreadable archive gets neither: its bytes are binary, and
// echoing them helps nobody reading a log or a download row.
func describeNonNZB(status int, b decodedBody) string {
	if b.unreadable {
		return fmt.Sprintf("the indexer returned HTTP %d with a compressed body that could not be decompressed", status)
	}
	if detail := newznabDetail(status, b.plain); detail != "" {
		return detail
	}
	if len(bytes.TrimSpace(b.plain)) == 0 {
		return fmt.Sprintf("the indexer returned HTTP %d with an empty body", status)
	}
	kind := "a body"
	if b.compressed {
		kind = "a compressed body"
	}
	return fmt.Sprintf("the indexer returned HTTP %d with %s that is not an NZB: %s", status, kind, snippet(b.plain))
}
