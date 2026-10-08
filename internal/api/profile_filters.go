package api

import (
	"context"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Reasons a metadata profile filter rejects a book. They travel in API
// responses, so they are stable identifiers rather than prose.
const (
	profileFilterPartBook        = "partBook"
	profileFilterMissingDate     = "missingDate"
	profileFilterLanguage        = "language"
	profileFilterMinEditionCount = "minEditionCount"
	profileFilterMinPages        = "minPages"
	profileFilterMissingISBN     = "missingIsbn"
)

// effectiveMetadataProfile returns the metadata profile that governs an
// author: its own, or the seeded default. Nil when profiles is nil or the
// lookup fails, which every caller treats as "filter nothing", the same
// fallback the author sync's resolve helpers use, so an unresolvable profile
// never turns into unexpected catalogue loss.
func effectiveMetadataProfile(ctx context.Context, profiles *db.MetadataProfileRepo, author *models.Author) *models.MetadataProfile {
	if profiles == nil {
		return nil
	}
	id := models.DefaultMetadataProfileID
	if author != nil && author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	p, err := profiles.GetByID(ctx, id)
	if err != nil {
		slog.Debug("metadata profile lookup failed; not filtering", "profileID", id, "error", err)
		return nil
	}
	return p
}

// storedBookProfileFilter reports which metadata profile filter rejects b,
// judged only from what a stored book row already carries: its title and its
// release date. "" means it passes. Series fill uses it so a fill does not put
// a stored row the profile rejects back on Wanted (#2208).
//
// These are the two filters the author sync applies without a provider round
// trip. The others need data a stored row does not hold reliably: language is
// decided from provider edition evidence that is not stored, and MinPages,
// SkipMissingISBN and MinEditionCount need a fresh edition lookup. Judging
// those from the row would skip books the sync correctly kept. Reconcile
// catalogue (catalogue_reconciliation.go) is the place that applies every
// filter to stored rows, with the provider lookups that needs.
func storedBookProfileFilter(p *models.MetadataProfile, b *models.Book) string {
	if p == nil || b == nil {
		return ""
	}
	if p.SkipPartBooks && isPartBookTitle(b.Title) {
		return profileFilterPartBook
	}
	if p.SkipMissingDate && b.ReleaseDate == nil {
		return profileFilterMissingDate
	}
	return ""
}

// catalogBookProfileFilter reports which metadata profile filter rejects a
// book a series fill is about to create, or "" when it passes. It applies the
// same filters, with the same "unknown passes" semantics, as the author sync's
// discovery loop in fetchAuthorBooks (#2208):
//
//   - SkipPartBooks and SkipMissingDate from the title and release date.
//   - MinEditionCount against the work's own edition count; zero is unknown
//     and passes.
//   - The language allow list, against the provider's language. A language the
//     list rejects is checked once more against the provider's edition
//     evidence, which the author sync also consults, so a work whose display
//     language is foreign but which has an allowed language edition is kept.
//   - MinPages and SkipMissingISBN from an edition lookup, made only when the
//     profile turns either on. A failed lookup enforces neither.
func catalogBookProfileFilter(ctx context.Context, meta *metadata.Aggregator, p *models.MetadataProfile, b *models.Book) string {
	if p == nil || b == nil {
		return ""
	}
	if reason := storedBookProfileFilter(p, b); reason != "" {
		return reason
	}
	if p.MinEditionCount > 0 && b.EditionCount > 0 && b.EditionCount < p.MinEditionCount {
		return profileFilterMinEditionCount
	}
	if allowed := models.ParseAllowedLanguages(p.AllowedLanguages); len(allowed) > 0 {
		unknownFail := p.UnknownLanguageBehavior == models.UnknownLanguageFail
		if !models.IsLanguageAllowed(b.Language, allowed, unknownFail) && !languageEvidenceAllows(ctx, meta, b, allowed) {
			return profileFilterLanguage
		}
	}
	if (p.MinPages > 0 || p.SkipMissingISBN) && meta != nil && b.ForeignID != "" {
		editions, err := meta.GetEditions(ctx, b.ForeignID)
		if err != nil {
			slog.Debug("edition lookup failed while checking MinPages/SkipMissingISBN; not enforcing for this book",
				"title", b.Title, "foreignId", b.ForeignID, "error", err)
			return ""
		}
		if p.SkipMissingISBN && !anyEditionHasISBN(editions) {
			return profileFilterMissingISBN
		}
		if p.MinPages > 0 && !passesMinPagesFilter(editions, p.MinPages) {
			return profileFilterMinPages
		}
	}
	return ""
}

// languageEvidenceAllows asks the provider whether any edition of b is in an
// allowed language. Only a definitive "allowed" answer counts; an error or an
// indeterminate answer leaves the scalar language's verdict standing.
func languageEvidenceAllows(ctx context.Context, meta *metadata.Aggregator, b *models.Book, allowed []string) bool {
	if meta == nil || b.ForeignID == "" {
		return false
	}
	evidence, err := meta.GetAuthorWorkLanguageEvidence(ctx, []models.Book{*b}, allowed)
	if err != nil {
		slog.Debug("language evidence lookup failed; using the book's own language", "title", b.Title, "error", err)
		return false
	}
	resolved, found := evidence[strings.TrimSpace(b.ForeignID)]
	return found && resolved.State == metadata.AuthorWorkLanguageAllowed
}
