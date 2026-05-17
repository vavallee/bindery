package metadata

import (
	"context"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// EnrichAudiobook fills narrator, duration, and cover from audnex when a
// book has audiobook audio (MediaType=audiobook or both) and a known ASIN.
// No-op otherwise.
func (a *Aggregator) EnrichAudiobook(ctx context.Context, book *models.Book) error {
	if book == nil || book.ASIN == "" {
		return nil
	}
	if book.MediaType != models.MediaTypeAudiobook && book.MediaType != models.MediaTypeBoth {
		return nil
	}
	b, err := a.getAudnexBookByASIN(ctx, book.ASIN)
	if err != nil || b == nil {
		return err
	}
	if narr := b.NarratorList(); narr != "" {
		book.Narrator = narr
	}
	if dur := b.DurationSeconds(); dur > 0 {
		book.DurationSeconds = dur
	}
	if book.ImageURL == "" && b.Image != "" {
		book.ImageURL = b.Image
	}
	if book.Description == "" && b.Summary != "" {
		book.Description = b.Summary
	}
	return nil
}

// GetCanonicalBookByASIN resolves an Audible ASIN through audnex, then uses the
// existing primary-provider canonicalizer to find the matching OpenLibrary work.
// It returns nil when audnex has no usable title/author data or the primary
// match is ambiguous.
func (a *Aggregator) GetCanonicalBookByASIN(ctx context.Context, asin string) (*models.Book, error) {
	asin = normalizeASIN(asin)
	if a == nil || a.primary == nil || asin == "" {
		return nil, nil
	}
	key := "asin-canonical:" + asin
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Book), nil
	}

	b, err := a.getAudnexBookByASIN(ctx, asin)
	if err != nil {
		return nil, err
	}
	if b == nil {
		var noBook *models.Book
		a.cache.set(key, noBook)
		return nil, nil
	}
	source := audnexBookToCanonicalSource(asin, b)
	if source == nil {
		var noBook *models.Book
		a.cache.set(key, noBook)
		return nil, nil
	}
	canonical, ok := a.canonicalPrimaryBook(ctx, "", *source)
	if !ok || canonical == nil {
		var noBook *models.Book
		a.cache.set(key, noBook)
		return nil, nil
	}
	if len(canonical.Description) < 50 || canonical.ImageURL == "" {
		a.enrichBook(ctx, canonical)
	}
	a.cache.set(key, canonical)
	return canonical, nil
}

func (a *Aggregator) getAudnexBookByASIN(ctx context.Context, asin string) (*audnex.Book, error) {
	asin = normalizeASIN(asin)
	if a == nil || a.audnex == nil || asin == "" {
		return nil, nil
	}
	key := "audnex-asin:" + asin
	if cached, ok := a.cache.get(key); ok {
		return cached.(*audnex.Book), nil
	}
	b, err := a.audnex.GetBook(ctx, asin)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, b)
	return b, nil
}

func audnexBookToCanonicalSource(asin string, b *audnex.Book) *models.Book {
	if b == nil {
		return nil
	}
	title := strings.TrimSpace(b.Title)
	if subtitle := strings.TrimSpace(b.Subtitle); subtitle != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(subtitle)) {
		if title == "" {
			title = subtitle
		} else {
			title += ": " + subtitle
		}
	}
	author := firstAudnexAuthorName(b.Authors)
	if title == "" || author == "" {
		return nil
	}
	sourceASIN := normalizeASIN(asin)
	if sourceASIN == "" {
		sourceASIN = normalizeASIN(b.ASIN)
	}
	return &models.Book{
		ForeignID:        "audnex:" + sourceASIN,
		Title:            title,
		SortTitle:        title,
		ASIN:             sourceASIN,
		MediaType:        models.MediaTypeAudiobook,
		Language:         normalizeAudibleLanguage(b.Language),
		MetadataProvider: "audnex",
		Author: &models.Author{
			Name: author,
		},
	}
}

func firstAudnexAuthorName(authors []audnex.Person) string {
	for _, author := range authors {
		if name := strings.TrimSpace(author.Name); name != "" {
			return name
		}
	}
	return ""
}

func normalizeASIN(asin string) string {
	return strings.ToUpper(strings.TrimSpace(asin))
}

func normalizeAudibleLanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" {
		return ""
	}
	if normalized, ok := audibleLanguageAliases[language]; ok {
		return normalized
	}
	return language
}

var audibleLanguageAliases = map[string]string{
	"english":    "eng",
	"german":     "ger",
	"french":     "fre",
	"spanish":    "spa",
	"italian":    "ita",
	"dutch":      "dut",
	"portuguese": "por",
	"japanese":   "jpn",
	"russian":    "rus",
	"chinese":    "chi",
	"danish":     "dan",
	"swedish":    "swe",
	"norwegian":  "nor",
	"polish":     "pol",
	"finnish":    "fin",
	"hindi":      "hin",
	"turkish":    "tur",
	"arabic":     "ara",
	"korean":     "kor",
	"czech":      "cze",
	"greek":      "gre",
}

// GetAuthorAudiobooks queries the Audible catalogue directly for the given
// author name. Returned books carry MediaType=audiobook, an ASIN, and a
// normalized language code; the caller applies the active metadata
// profile's allowed_languages filter alongside OpenLibrary-sourced books.
//
// Callers use this as a supplement to GetAuthorWorks — neither OpenLibrary
// nor Hardcover has full Audible ASIN cross-referencing, so prolific
// authors (Sanderson, King, etc.) are missing a large share of their
// narrated catalogue without a direct Audible source.
//
// Returns an empty slice when the audible client is unconfigured (test
// aggregators) rather than nil-derefing. Errors propagate so the caller
// can log them without failing the broader ingestion.
func (a *Aggregator) GetAuthorAudiobooks(ctx context.Context, authorName string) ([]models.Book, error) {
	if a.audible == nil {
		return nil, nil
	}
	authorName = strings.TrimSpace(authorName)
	if authorName == "" {
		return nil, nil
	}
	key := "audible-author:" + strings.ToLower(authorName)
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Book), nil
	}
	books, err := a.audible.SearchBooksByAuthor(ctx, authorName)
	if err != nil {
		return nil, err
	}
	if books == nil {
		books = []models.Book{}
	}
	a.cache.set(key, books)
	return books, nil
}

func (a *Aggregator) enrichBook(ctx context.Context, book *models.Book) {
	for _, enricher := range a.enrichers {
		enriched, err := enricher.SearchBooks(ctx, book.Title)
		if err != nil {
			slog.Debug("enrichment failed", "provider", enricher.Name(), "error", err)
			continue
		}
		// Pick the first result that plausibly matches our book — same
		// title AND (if we have one) same author. Without the author
		// guard a German title like "Die Verwandlung" could pull the
		// wrong author's record off OL; refusing to enrich is safer
		// than enriching with wrong data. Issue #667.
		e := pickEnrichmentMatch(enriched, book)
		if e == nil {
			continue
		}
		if len(e.Description) > len(book.Description) {
			book.Description = e.Description
			slog.Debug("enriched description", "provider", enricher.Name(), "book", book.Title)
		}
		if book.AverageRating == 0 && e.AverageRating > 0 {
			book.AverageRating = e.AverageRating
			book.RatingsCount = e.RatingsCount
		}
		if book.ImageURL == "" && e.ImageURL != "" {
			book.ImageURL = e.ImageURL
			slog.Debug("enriched cover", "provider", enricher.Name(), "book", book.Title)
		}
	}

	// Cover-only fallback: any provider implementing CoverProvider gets
	// a chance to serve a cover by ISBN when enrichers above didn't
	// supply one. Currently only DNB (its MVB cover service is separate
	// from the SRU bibliographic endpoint and has covers SRU doesn't).
	// Skipped entirely when ImageURL is already set.
	if book.ImageURL == "" {
		a.fillCoverFromCoverProviders(ctx, book)
	}
}

// pickEnrichmentMatch returns the first candidate that plausibly matches
// target — same title (case-insensitive substring either way to tolerate
// subtitles like ": Roman") AND, when target carries an author, the
// candidate's author name overlaps too. Returns nil when no candidate
// matches. Conservative by design: a false negative just leaves
// enrichment empty; a false positive overwrites the user's book with
// data from an unrelated record.
func pickEnrichmentMatch(candidates []models.Book, target *models.Book) *models.Book {
	targetTitle := strings.ToLower(strings.TrimSpace(target.Title))
	if targetTitle == "" {
		return nil
	}
	targetAuthor := ""
	if target.Author != nil {
		targetAuthor = strings.ToLower(strings.TrimSpace(target.Author.Name))
	}
	for i := range candidates {
		c := &candidates[i]
		cTitle := strings.ToLower(strings.TrimSpace(c.Title))
		if cTitle == "" {
			continue
		}
		if !strings.Contains(cTitle, targetTitle) && !strings.Contains(targetTitle, cTitle) {
			continue
		}
		if targetAuthor == "" {
			return c
		}
		if c.Author == nil {
			continue
		}
		cAuthor := strings.ToLower(strings.TrimSpace(c.Author.Name))
		if cAuthor == "" {
			continue
		}
		if !strings.Contains(cAuthor, targetAuthor) && !strings.Contains(targetAuthor, cAuthor) {
			continue
		}
		return c
	}
	return nil
}

// fillCoverFromCoverProviders walks every provider that implements
// CoverProvider and tries each ISBN edition until one resolves. Used by
// enrichBook as a last-resort cover lookup for books whose primary
// provider (e.g. DNB) returns no cover URL in its bibliographic data.
func (a *Aggregator) fillCoverFromCoverProviders(ctx context.Context, book *models.Book) {
	for _, p := range a.providers() {
		cp, ok := p.(CoverProvider)
		if !ok {
			continue
		}
		for _, ed := range book.Editions {
			var isbn string
			switch {
			case ed.ISBN13 != nil && *ed.ISBN13 != "":
				isbn = *ed.ISBN13
			case ed.ISBN10 != nil && *ed.ISBN10 != "":
				isbn = *ed.ISBN10
			}
			if isbn == "" {
				continue
			}
			if url := cp.CoverByISBN(ctx, isbn); url != "" {
				book.ImageURL = url
				slog.Debug("enriched cover via CoverProvider",
					"provider", p.Name(), "isbn", isbn, "book", book.Title)
				return
			}
		}
	}
}
