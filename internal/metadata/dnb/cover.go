package dnb

import (
	"context"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/useragent"
)

// mvbCoverEndpoint is DNB's public cover-image service, separate from the
// SRU bibliographic endpoint. MARC records via SRU do not include cover
// URLs; this endpoint serves a JPEG for many German-edition ISBNs by
// looking them up in the MVB catalogue. No API key required.
const mvbCoverEndpoint = "https://portal.dnb.de/opac/mvb/cover"

// CoverByISBN implements metadata.CoverProvider. Returns the MVB cover URL
// for isbn when the service confirms (via HEAD) that it serves an image
// for that ISBN; returns "" on any failure (404, non-image content-type,
// network error, malformed ISBN). The returned URL is safe to embed
// directly in book.ImageURL — it stays valid as long as the ISBN is in
// MVB's catalogue.
//
// Pattern lifted from calibre-dnb's `_get_validated_cover_url` (issue
// #667). Cheap: HEAD only, no body transfer.
func (c *Client) CoverByISBN(ctx context.Context, isbn string) string {
	digits := stripISBNQualifier(isbn)
	if digits == "" {
		return ""
	}
	url := mvbCoverEndpoint + "?isbn=" + digits
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", useragent.Get())
	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "image/") {
		return ""
	}
	return url
}
