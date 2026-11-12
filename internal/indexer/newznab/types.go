package newznab

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// IndexerError is a structured error returned by a Newznab/Torznab indexer via
// its <error code="N" description="..."/> response element. It carries the raw
// numeric code so callers can classify the failure:
//
//   - 1xx codes (100–199) indicate authentication / authorization problems.
//   - 5xx codes (500–599) indicate rate-limiting by the indexer.
//   - Other codes (200–499, 9xx, …) are indexer-defined operational errors.
//
// Callers that only need the human-readable message can treat IndexerError like
// any other error via its Error() method.
type IndexerError struct {
	Code        int
	Description string
}

func (e *IndexerError) Error() string {
	switch {
	case e.Code == 0 && e.Description == "":
		return "indexer error"
	case e.Code == 0:
		return fmt.Sprintf("indexer error: %s", e.Description)
	case e.Description == "":
		return fmt.Sprintf("indexer error %d", e.Code)
	default:
		return fmt.Sprintf("indexer error %d: %s", e.Code, e.Description)
	}
}

// IsAuthError reports whether the error is an authentication / authorization
// failure (Newznab 1xx code range: 100 = bad credentials, 101 = account
// suspended, 102 = VPN forbidden, etc.).
func IsAuthError(err error) bool {
	var ie *IndexerError
	if !errors.As(err, &ie) {
		return false
	}
	return ie.Code >= 100 && ie.Code <= 199
}

// HTTPStatusError is a non-200 response that carried no Newznab <error>
// document: a Cloudflare block page, a maintenance page, a proxy error. It
// keeps the status so a rate limit applied at the HTTP layer classifies the
// same way as one the indexer reports in XML (#2635), and the Retry-After
// header when the server sent one.
type HTTPStatusError struct {
	Status int
	// RetryAfter is the parsed Retry-After header, or 0 when it was absent or
	// unreadable.
	RetryAfter time.Duration
	// Snippet is the start of the response body, for the log line.
	Snippet string
}

func (e *HTTPStatusError) Error() string {
	if e.Snippet == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Snippet)
}

// parseRetryAfter reads a Retry-After header, which is either a delay in
// seconds or an HTTP date. Returns 0 for anything else, including a date in
// the past.
func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// IsRateLimitError reports whether the error is a rate-limit rejection: a
// Newznab 5xx code (500 = request limit reached, 520 = maximum grabs reached,
// etc.), an HTTP 429, or an HTTP 503 that names a Retry-After. A 503 on its
// own stays a transient error: it is usually maintenance, and the tier
// fall-through and the next search should still try (#2635).
func IsRateLimitError(err error) bool {
	var ie *IndexerError
	if errors.As(err, &ie) {
		return ie.Code >= 500 && ie.Code <= 599
	}
	var he *HTTPStatusError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests ||
			(he.Status == http.StatusServiceUnavailable && he.RetryAfter > 0)
	}
	return false
}

// IsHardIndexerError reports whether err is an error that the indexer itself
// deliberately returned (auth failure or rate limit), as opposed to a transient
// network or decoding problem. Callers can use this to abort tier fall-through:
// if an indexer has explicitly rejected the session, retrying lower tiers
// against the same indexer is wasteful and will produce the same result.
func IsHardIndexerError(err error) bool {
	return IsAuthError(err) || IsRateLimitError(err)
}

// Newznab RSS response
type rssResponse struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title    string      `xml:"title"`
	Response nzbResponse `xml:"response"`
	Items    []rssItem   `xml:"item"`
}

type nzbResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type rssItem struct {
	Title     string       `xml:"title"`
	GUID      rssGUID      `xml:"guid"`
	Link      string       `xml:"link"`
	Comments  string       `xml:"comments"`
	PubDate   string       `xml:"pubDate"`
	Category  string       `xml:"category"`
	Enclosure rssEnclosure `xml:"enclosure"`
	Attrs     []nzbAttr    `xml:"attr"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type nzbAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// Caps response
type capsResponse struct {
	XMLName    xml.Name       `xml:"caps"`
	Searching  capsSearching  `xml:"searching"`
	Categories capsCategories `xml:"categories"`
}

type capsSearching struct {
	Search     capsSearch `xml:"search"`
	BookSearch capsSearch `xml:"book-search"`
}

type capsSearch struct {
	Available string `xml:"available,attr"`
}

type capsCategories struct {
	Categories []capsCategory `xml:"category"`
}

type capsCategory struct {
	ID      string         `xml:"id,attr"`
	Name    string         `xml:"name,attr"`
	SubCats []capsCategory `xml:"subcat"`
}

// SearchResult is the domain type for an indexer search result.
type SearchResult struct {
	GUID        string `json:"guid"`
	IndexerID   int64  `json:"indexerId"`
	IndexerName string `json:"indexerName"`
	Title       string `json:"title"`
	Size        int64  `json:"size"`
	PubDate     string `json:"pubDate"`
	NZBURL      string `json:"nzbUrl"`
	InfoURL     string `json:"infoUrl,omitempty"` // human-readable indexer detail/release page (from comments, guid-permalink, or link); never the download URL
	Category    string `json:"category"`
	Grabs       int    `json:"grabs"`
	Author      string `json:"author"`
	BookTitle   string `json:"bookTitle"`
	Protocol    string `json:"protocol"`           // "usenet" or "torrent"
	Language    string `json:"language,omitempty"` // ISO 639-1 from newznab:attr language (when present)
	// DownloadVolumeFactor is the torznab downloadvolumefactor attribute: the
	// fraction of this release's size that counts against the user's download
	// ratio. 0 means freeleech (costs nothing), 1 is a normal release, 0.5 is
	// half-leech. nil means the indexer did not report it — usenet indexers
	// never do, and it is absent from most public torznab feeds.
	DownloadVolumeFactor *float64 `json:"downloadVolumeFactor,omitempty"`
	MediaType            string   `json:"mediaType,omitempty"` // "ebook" or "audiobook"; set for dual-format book searches
	IndexerPriority      int      `json:"indexerPriority"`     // copied from models.Indexer.Priority; higher wins

	// Seeders and Peers are the torznab seeders and peers attributes: the
	// release's current seed count, and seeders plus leechers. Unlike Grabs,
	// a lifetime download count, they say whether the swarm is alive now.
	// nil means the indexer did not report them (usenet indexers never do),
	// which is not the same as a reported 0, a dead torrent.
	Seeders *int `json:"seeders,omitempty"`
	Peers   *int `json:"peers,omitempty"`
}
