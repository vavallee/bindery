package opds

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// ErrNotFound is returned by Build* methods when a requested entity (author,
// series, book) doesn't exist. Handlers translate it to HTTP 404.
var ErrNotFound = errors.New("opds: not found")

// navFeed returns a navigation-kind feed pre-populated with namespaces.
func navFeed(title, id, updated string) Feed {
	return Feed{
		Xmlns:           NSAtom,
		XmlnsDC:         NSDC,
		XmlnsOPDS:       NSOPDS,
		XmlnsOpenSearch: NSOpenSearch,
		ID:              id,
		Title:           title,
		Updated:         updated,
	}
}

// acquisitionFeed returns an acquisition-kind feed pre-populated with
// namespaces.
func acquisitionFeed(title, id, updated string) Feed {
	return navFeed(title, id, updated)
}

// descriptionContent converts a possibly-multiline plain-text description
// into an Atom <content type="text"> element, or nil when empty.
func descriptionContent(desc string) *Content {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return nil
	}
	return &Content{Type: "text", Body: desc}
}

// nonEmpty returns the first non-empty value. If none of the candidates is
// non-empty the last one is returned (so callers always get a usable fallback).
func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// rfc3339 formats a time as RFC3339 in UTC. Zero times become the epoch,
// since Atom requires <updated> to be a valid xsd:dateTime.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return "1970-01-01T00:00:00Z"
	}
	return t.UTC().Format(time.RFC3339)
}

// nowRFC3339 is a test seam for the builder's "now" timestamp.
var nowRFC3339 = func() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// normalizePage clamps page numbers to [1, ∞). OPDS page query params are
// 1-based per convention.
func normalizePage(p int) int {
	if p < 1 {
		return 1
	}
	return p
}

// pageBounds returns the (inclusive, exclusive) slice indexes for the given
// 1-based page. When page overruns the total, returns an empty window.
func pageBounds(page, size, total int) (start, end int) {
	start = (page - 1) * size
	if start >= total {
		return total, total
	}
	end = min(start+size, total)
	return start, end
}

// pagedURL returns base (optionally with ?page=N when N > 1). The OPDS
// convention is that the unpaginated URL is equivalent to page=1.
func pagedURL(base string, page int) string {
	if page <= 1 {
		return base
	}
	return fmt.Sprintf("%s?page=%d", base, page)
}

// addPagingLinks appends rel="next" / rel="previous" links to the feed
// when more pages exist.
func addPagingLinks(f *Feed, base string, page, size, total int) {
	if total <= 0 {
		return
	}
	maxPage := (total + size - 1) / size
	if page < maxPage {
		f.Links = append(f.Links, Link{
			Rel: RelNext, Href: pagedURL(base, page+1), Type: TypeNavigation,
		})
	}
	if page > 1 {
		f.Links = append(f.Links, Link{
			Rel: RelPrevious, Href: pagedURL(base, page-1), Type: TypeNavigation,
		})
	}
}

// filterImported keeps only books whose file actually exists in the
// library (status=imported and file_path set). OPDS is a download
// catalogue — we must not advertise links that will 404.
func filterImported(books []models.Book) []models.Book {
	kept := books[:0]
	for _, b := range books {
		if b.Status == models.BookStatusImported && b.FilePath != "" {
			kept = append(kept, b)
		}
	}
	return kept
}

// guessFileType maps a stored file path to the MIME type OPDS clients
// expect in the acquisition link. Audiobooks come down as a zip of the
// folder (see api.FileHandler.Download); ebooks match their extension.
func guessFileType(path, mediaType string) string {
	if mediaType == models.MediaTypeAudiobook {
		return "application/zip"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".epub":
		return "application/epub+zip"
	case ".pdf":
		return "application/pdf"
	case ".mobi":
		return "application/x-mobipocket-ebook"
	case ".azw", ".azw3":
		return "application/vnd.amazon.ebook"
	case ".cbz":
		return "application/vnd.comicbook+zip"
	case ".cbr":
		return "application/vnd.comicbook-rar"
	case ".txt":
		return "text/plain"
	case ".fb2":
		return "application/x-fictionbook+xml"
	default:
		return "application/octet-stream"
	}
}

// guessImageType returns the MIME type for a cover image URL by extension.
// When we can't tell, fall back to image/jpeg — the dominant format on the
// provider CDNs Bindery reads from (OpenLibrary, Google Books, Hardcover).
func guessImageType(u string) string {
	switch strings.ToLower(filepath.Ext(strings.Split(u, "?")[0])) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}
