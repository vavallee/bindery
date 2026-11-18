package audiblesync

import (
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
)

// IDPrefix is the ForeignID namespace of books mapped from Audible Sync. It
// is the Audible one: the ASIN names the same product whichever door it came
// through, and a book first found by the direct Audible client must dedupe
// against the same book found through Audible Sync.
const IDPrefix = "audible:"

// providerName is what a mapped book records as its MetadataProvider. It is
// "audible", the same as the direct client's, for the same reason as
// IDPrefix: the data is Audible's, Audible Sync only relays it.
const providerName = "audible"

// ToBook maps a catalogue book onto a Bindery book: an audiobook keyed by its
// ASIN. It returns nil for an entry with no ASIN or no title.
func ToBook(cb CatalogBook) *models.Book {
	asin := isbnutil.NormalizeASIN(cb.ASIN)
	title := strings.TrimSpace(cb.Title)
	if asin == "" || title == "" {
		return nil
	}
	if sub := strings.TrimSpace(cb.Subtitle); sub != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(sub)) {
		title += ": " + sub
	}
	b := &models.Book{
		ForeignID:        IDPrefix + asin,
		Title:            title,
		SortTitle:        title,
		ASIN:             asin,
		MediaType:        models.MediaTypeAudiobook,
		Language:         models.NormalizeLanguageCode(cb.Language),
		Narrator:         joinNames(cb.Narrators),
		ImageURL:         strings.TrimSpace(cb.CoverURL),
		MetadataProvider: providerName,
		Status:           models.BookStatusWanted,
		Monitored:        true,
		Genres:           nonEmpty(cb.Genres),
		ReleaseDate:      ParseReleaseDate(cb.ReleaseDate),
		RatingsCount:     cb.RatingCount,
	}
	if cb.LengthSeconds != nil && *cb.LengthSeconds > 0 {
		b.DurationSeconds = *cb.LengthSeconds
	}
	if cb.Rating != nil {
		b.AverageRating = *cb.Rating
	}
	if name := FirstAuthor(cb); name != "" {
		b.Author = &models.Author{Name: name}
	}
	b.SeriesRefs = SeriesRefs(cb)
	return b
}

// FirstAuthor is the book's first named author.
func FirstAuthor(cb CatalogBook) string {
	for _, a := range cb.Authors {
		if name := strings.TrimSpace(a.Name); name != "" {
			return name
		}
	}
	return ""
}

// SeriesRefs turns a book's series into the refs Bindery links on create,
// the primary series first and marked. A series with no ASIN gets no ref:
// without one there is nothing stable to key it by.
func SeriesRefs(cb CatalogBook) []models.SeriesRef {
	var refs []models.SeriesRef
	seen := map[string]bool{}
	primary := ""
	if cb.PrimarySeries != nil {
		primary = isbnutil.NormalizeASIN(cb.PrimarySeries.SeriesASIN)
	}
	for i, s := range cb.Series {
		asin := isbnutil.NormalizeASIN(s.SeriesASIN)
		title := strings.TrimSpace(s.Title)
		if asin == "" || title == "" || seen[asin] {
			continue
		}
		seen[asin] = true
		refs = append(refs, models.SeriesRef{
			ForeignID: SeriesIDPrefix + asin,
			Title:     title,
			Position:  strings.TrimSpace(s.Sequence),
			Primary:   asin == primary || (primary == "" && i == 0),
		})
	}
	return refs
}

// SeriesIDPrefix namespaces series keyed by Audible's series ASIN.
const SeriesIDPrefix = "audible-series:"

// ParseReleaseDate reads Audible's release date, which Audible Sync passes
// through as Audible wrote it: normally YYYY-MM-DD.
func ParseReleaseDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

func joinNames(names []string) string {
	return strings.Join(nonEmpty(names), ", ")
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
