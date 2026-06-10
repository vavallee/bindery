// Package bookhydrate persists metadata-provider edition details for newly
// created or rebound books.
package bookhydrate

import (
	"context"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// EditionFetcher fetches provider editions for the given book foreign ID.
type EditionFetcher func(context.Context, string) ([]models.Edition, error)

// EditionUpserter stores metadata editions without overwriting existing
// non-empty imported or curated fields.
type EditionUpserter interface {
	UpsertMetadata(context.Context, *models.Edition) (bool, error)
}

// BookUpdater persists book-level fields promoted during hydration.
type BookUpdater interface {
	Update(context.Context, *models.Book) error
}

// AudiobookEnricher fills audiobook metadata once an ASIN is known.
type AudiobookEnricher interface {
	EnrichAudiobook(context.Context, *models.Book) error
}

// Options describes a single Hardcover edition hydration attempt.
type Options struct {
	Book              *models.Book
	Provider          string
	ProviderForeignID string
	Editions          EditionUpserter
	Books             BookUpdater
	FetchEditions     EditionFetcher
	Enricher          AudiobookEnricher
}

// Result summarizes a best-effort hydration attempt.
type Result struct {
	Fetched           int
	Upserted          int
	ASINPromoted      bool
	MetadataDerived   bool
	AudiobookEnriched bool
	BookUpdated       bool
	Err               error
}

// IsHardcoverBook reports whether a book has a confident Hardcover identity.
func IsHardcoverBook(book *models.Book, provider string) bool {
	if strings.EqualFold(strings.TrimSpace(provider), "hardcover") {
		return true
	}
	if book == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(book.MetadataProvider), "hardcover") {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(book.ForeignID), "hc:")
}

// HydrateHardcoverEditions fetches and persists Hardcover editions for a
// confident Hardcover book. All failures are logged and reflected in Result.Err
// but are non-fatal to callers.
func HydrateHardcoverEditions(ctx context.Context, opts Options) Result {
	var result Result
	book := opts.Book
	if book == nil || book.ID == 0 {
		return result
	}
	editionForeignID := strings.TrimSpace(opts.ProviderForeignID)
	if editionForeignID == "" {
		editionForeignID = book.ForeignID
	}
	if !IsHardcoverBook(book, opts.Provider) && !strings.HasPrefix(editionForeignID, "hc:") {
		return result
	}
	if opts.Editions == nil || opts.FetchEditions == nil {
		return result
	}

	editions, err := opts.FetchEditions(ctx, editionForeignID)
	if err != nil {
		result.Err = err
		slog.Warn("hardcover edition hydration failed", "bookID", book.ID, "foreignID", editionForeignID, "bookForeignID", book.ForeignID, "error", err)
		return result
	}
	result.Fetched = len(editions)

	acceptedAudioEditions := make([]models.Edition, 0, len(editions))
	for i := range editions {
		edition := editions[i]
		edition.BookID = book.ID
		if strings.TrimSpace(edition.Title) == "" {
			edition.Title = book.Title
		}
		ok, err := opts.Editions.UpsertMetadata(ctx, &edition)
		if err != nil {
			if result.Err == nil {
				result.Err = err
			}
			slog.Warn("hardcover edition upsert failed", "bookID", book.ID, "foreignID", editionForeignID, "bookForeignID", book.ForeignID, "editionID", edition.ForeignID, "error", err)
			continue
		}
		if !ok {
			slog.Debug("hardcover edition skipped because it belongs to another book", "bookID", book.ID, "foreignID", editionForeignID, "bookForeignID", book.ForeignID, "editionID", edition.ForeignID)
			continue
		}
		result.Upserted++
		if isLikelyAudioEdition(edition) {
			acceptedAudioEditions = append(acceptedAudioEditions, edition)
		}
	}

	// #806: derive audiobook metadata from the chosen Hardcover edition BEFORE
	// running Audnex, so Hardcover's deterministic edition data fills the book
	// and Audnex is left to cover only the gaps (narrator, refined duration,
	// summary, cover-if-missing). preferredAudioEdition picks the same edition
	// maybePromoteASIN promotes the ASIN from, keeping the two derivations
	// consistent.
	if edition, ok := preferredAudioEdition(acceptedAudioEditions); ok {
		if deriveAudiobookMetadataFromEdition(book, edition) {
			result.MetadataDerived = true
		}
	}

	if maybePromoteASIN(book, acceptedAudioEditions) {
		result.ASINPromoted = true
		// Audnex enrichment runs AFTER Hardcover-edition derivation (above) so
		// it only fills what Hardcover lacked (#806).
		if opts.Enricher != nil {
			if err := opts.Enricher.EnrichAudiobook(ctx, book); err != nil {
				if result.Err == nil {
					result.Err = err
				}
				slog.Debug("hardcover ASIN audiobook enrichment skipped", "bookID", book.ID, "asin", book.ASIN, "error", err)
			} else {
				result.AudiobookEnriched = true
			}
		}
	}

	// Persist when Hardcover-edition derivation or ASIN promotion changed the
	// book. Derivation alone (e.g. ASIN already set) is enough to warrant a
	// write so the language/cover/duration we pulled isn't lost.
	if (result.ASINPromoted || result.MetadataDerived) && opts.Books != nil {
		if err := opts.Books.Update(ctx, book); err != nil {
			if result.Err == nil {
				result.Err = err
			}
			slog.Warn("hardcover book hydration persist failed", "bookID", book.ID, "asin", book.ASIN, "error", err)
		} else {
			result.BookUpdated = true
		}
	}

	return result
}

// preferredAudioEdition returns the audio-looking edition the hydrator should
// derive book metadata from — the same edition maybePromoteASIN promotes the
// ASIN from (highest audioEditionScore, first on ties). Returns ok=false when
// there is no audio edition to derive from.
func preferredAudioEdition(editions []models.Edition) (models.Edition, bool) {
	best := -1
	bestScore := -1
	for i := range editions {
		score := audioEditionScore(editions[i])
		if best == -1 || score > bestScore {
			best = i
			bestScore = score
		}
	}
	if best == -1 {
		return models.Edition{}, false
	}
	return editions[best], true
}

// deriveAudiobookMetadataFromEdition fills book-level audiobook fields from a
// Hardcover edition, preferring Hardcover's deterministic edition data before
// Audnex runs (#806). It only ever fills unknown fields — known values are
// never overwritten ("unknown ⇒ don't clobber known"). It also makes sure an
// audio-bearing book carries an audiobook MediaType so the Audnex path is
// eligible. Returns whether it changed anything.
func deriveAudiobookMetadataFromEdition(book *models.Book, edition models.Edition) bool {
	if book == nil {
		return false
	}
	changed := false

	if !bookAcceptsAudiobookASIN(book) {
		// The chosen edition is audio-looking but the book wasn't flagged as
		// audio yet; promote it so downstream audio enrichment is eligible.
		switch book.MediaType {
		case "":
			book.MediaType = models.MediaTypeAudiobook
			changed = true
		case models.MediaTypeEbook:
			book.MediaType = models.MediaTypeBoth
			changed = true
		}
	}

	if book.Language == "" {
		if lang := strings.TrimSpace(edition.Language); lang != "" {
			book.Language = lang
			changed = true
		}
	}

	if book.ImageURL == "" {
		if cover := strings.TrimSpace(edition.ImageURL); cover != "" {
			book.ImageURL = cover
			changed = true
		}
	}

	return changed
}

func maybePromoteASIN(book *models.Book, editions []models.Edition) bool {
	if book == nil || strings.TrimSpace(book.ASIN) != "" || !bookAcceptsAudiobookASIN(book) {
		return false
	}
	asin := preferredEditionASIN(editions)
	if asin == "" {
		return false
	}
	book.ASIN = asin
	return true
}

func bookAcceptsAudiobookASIN(book *models.Book) bool {
	return book.MediaType == models.MediaTypeAudiobook || book.MediaType == models.MediaTypeBoth
}

func preferredEditionASIN(editions []models.Edition) string {
	bestASIN := ""
	bestScore := -1
	for _, edition := range editions {
		if edition.ASIN == nil {
			continue
		}
		asin := strings.ToUpper(strings.TrimSpace(*edition.ASIN))
		if asin == "" {
			continue
		}
		score := audioEditionScore(edition)
		if bestASIN == "" || score > bestScore {
			bestASIN = asin
			bestScore = score
		}
	}
	return bestASIN
}

func audioEditionScore(edition models.Edition) int {
	text := strings.ToLower(strings.Join([]string{
		edition.Format,
		edition.EditionInfo,
	}, " "))
	score := 0
	if editionHasAudioMarker(text) {
		score += 10
	}
	if !edition.IsEbook {
		score++
	}
	return score
}

func isLikelyAudioEdition(edition models.Edition) bool {
	text := strings.ToLower(strings.Join([]string{
		edition.Format,
		edition.EditionInfo,
	}, " "))
	return editionHasAudioMarker(text)
}

func editionHasAudioMarker(text string) bool {
	for _, marker := range []string{"audio", "audible", "mp3", "cd", "cassette"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
