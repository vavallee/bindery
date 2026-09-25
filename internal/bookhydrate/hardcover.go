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
	FillMissingAudiobookDuration(context.Context, *models.Book) (bool, int, error)
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
	// MediaTypePinned marks the book's MediaType as a deliberate caller
	// choice (e.g. an import list's per-list format override) rather than a
	// provider-derived guess. Hydration must not widen a pinned single-format
	// book to "both" just because an audio-shaped edition exists (#1732).
	MediaTypePinned bool
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
	before := *book
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
		// UpsertMetadata reloads persisted fields; runtime exists only in the provider response.
		edition.DurationSeconds = editions[i].DurationSeconds
		if !isLikelyAudioEdition(editions[i]) {
			// A retained unknown format must not hide a fetched print format and
			// turn that print edition into audio solely through its runtime.
			edition.DurationSeconds = 0
		}
		if isLikelyAudioEdition(edition) {
			if editions[i].ASIN != nil && edition.ASIN != nil && strings.TrimSpace(*editions[i].ASIN) != "" &&
				!strings.EqualFold(strings.TrimSpace(*editions[i].ASIN), strings.TrimSpace(*edition.ASIN)) {
				// The stored ASIN belongs to a different narration than the fetched runtime.
				edition.DurationSeconds = 0
			}
			acceptedAudioEditions = append(acceptedAudioEditions, edition)
		}
	}

	// #806: derive audiobook metadata from the chosen Hardcover edition BEFORE
	// running Audnex, so its edition data fills missing book fields. Audnex can
	// then refine duration and fill narrator, summary, or a missing cover.
	// Prefer the edition matching the book's ASIN or the one promoted below.
	if edition, ok := preferredAudioEdition(acceptedAudioEditions, book.ASIN); ok {
		if strings.TrimSpace(book.ASIN) != "" && (edition.ASIN == nil || !strings.EqualFold(strings.TrimSpace(*edition.ASIN), strings.TrimSpace(book.ASIN))) {
			// A different known ASIN may be a different narration; leave runtime unknown.
			edition.DurationSeconds = 0
		}
		if deriveAudiobookMetadataFromEdition(book, edition, opts.MediaTypePinned) {
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
		var err error
		updated := false
		if !result.ASINPromoted && before.DurationSeconds <= 0 && book.DurationSeconds > 0 &&
			before.ASIN == book.ASIN && before.MediaType == book.MediaType && before.Status == book.Status &&
			before.Language == book.Language && before.ImageURL == book.ImageURL {
			var currentDuration int
			updated, currentDuration, err = opts.Books.FillMissingAudiobookDuration(ctx, book)
			if !updated {
				book.DurationSeconds = currentDuration
				result.MetadataDerived = false
				if err == nil {
					slog.Debug("hardcover duration write skipped after concurrent book update", "bookID", book.ID)
				}
			}
		} else {
			err = opts.Books.Update(ctx, book)
			updated = err == nil
		}
		if err != nil {
			if result.Err == nil {
				result.Err = err
			}
			slog.Warn("hardcover book hydration persist failed", "bookID", book.ID, "asin", book.ASIN, "error", err)
		} else {
			result.BookUpdated = updated
		}
	}

	return result
}

// preferredAudioEdition returns the audio-looking edition whose ASIN matches
// the book, or the edition maybePromoteASIN will use when the book has no ASIN.
// Among matching ASINs, prefer the highest audioEditionScore, then an edition
// with a known runtime (first on ties).
func preferredAudioEdition(editions []models.Edition, bookASIN string) (models.Edition, bool) {
	best := -1
	targetASIN := strings.TrimSpace(bookASIN)
	if targetASIN == "" {
		targetASIN = preferredEditionASIN(editions)
	}
	for i := range editions {
		matchesASIN := targetASIN != "" && editions[i].ASIN != nil && strings.EqualFold(strings.TrimSpace(*editions[i].ASIN), targetASIN)
		if best == -1 {
			best = i
			continue
		}
		bestMatchesASIN := targetASIN != "" && editions[best].ASIN != nil && strings.EqualFold(strings.TrimSpace(*editions[best].ASIN), targetASIN)
		if matchesASIN != bestMatchesASIN {
			if matchesASIN {
				best = i
			}
			continue
		}
		score, bestScore := audioEditionScore(editions[i]), audioEditionScore(editions[best])
		if score != bestScore {
			if score > bestScore {
				best = i
			}
			continue
		}
		if (matchesASIN || targetASIN == "") && (editions[i].DurationSeconds > 0) != (editions[best].DurationSeconds > 0) {
			if editions[i].DurationSeconds > 0 {
				best = i
			}
			continue
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
// never overwritten ("unknown ⇒ don't clobber known"), and a field the user
// locked is left alone even when empty, because clearing it was a manual edit
// (#2757). Emptiness and ownership are separate tests: Book.CanWrite answers
// only the second (#2767). It also makes sure an audio-bearing book carries
// an audiobook MediaType so the Audnex path is eligible. Returns whether it
// changed anything.
//
// mediaTypePinned means the caller set MediaType deliberately (a list's
// per-list format override): the promotion below must not run, or an
// ebook-pinned book would be widened to "both" whenever the work has any
// audio edition on Hardcover — true for most popular titles (#1732). The
// "" → audiobook arm is unaffected because a pinned MediaType is by
// definition non-empty.
func deriveAudiobookMetadataFromEdition(book *models.Book, edition models.Edition, mediaTypePinned bool) bool {
	if book == nil {
		return false
	}
	changed := false

	if !bookAcceptsAudiobookASIN(book) && !mediaTypePinned {
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
		if changed {
			// The promotion adds a monitored format with no file behind it. If
			// status stays 'imported' the gap is invisible to the wanted page,
			// the scheduled sweep and the author bulk search, all of which
			// select on status alone (#1634).
			book.ReevaluateStatus()
		}
	}

	if book.Language == "" && book.CanWrite(models.BookFieldLanguage) {
		if lang := strings.TrimSpace(edition.Language); lang != "" {
			book.Language = lang
			changed = true
		}
	}

	// ImageURL is deliberately unguarded: it is not in
	// models.LockableBookFields and there is no way for a user to lock it,
	// because the edit dialog has no cover field to lock by editing. Adding it
	// to the lockable set would be a user visible capability change needing the
	// edit UI and docs/Metadata-Editing-Wiki.md to match, so it stays a
	// fill-empty write (#2767).
	if book.ImageURL == "" {
		if cover := strings.TrimSpace(edition.ImageURL); cover != "" {
			book.ImageURL = cover
			changed = true
		}
	}
	if bookAcceptsAudiobookASIN(book) && book.DurationSeconds <= 0 && edition.DurationSeconds > 0 {
		book.DurationSeconds = edition.DurationSeconds
		changed = true
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
	format := strings.TrimSpace(edition.Format)
	unknownFormat := format == "" || strings.EqualFold(format, "unknown")
	return editionHasAudioMarker(text) || (unknownFormat && edition.DurationSeconds > 0 && !edition.IsEbook)
}

func editionHasAudioMarker(text string) bool {
	for _, marker := range []string{"audio", "audible", "mp3", "cd", "cassette"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
