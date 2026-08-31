package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

const (
	reconcileReasonProviderChanged = "provider_changed"
	reconcileReasonNotInCatalogue  = "not_in_current_catalogue"
	reconcileReasonLanguage        = "language_not_allowed"
	reconcileReasonPartBook        = "part_book"
	reconcileReasonMissingDate     = "missing_release_date"
	reconcileReasonPopularity      = "below_minimum_popularity"
	reconcileReasonPages           = "below_minimum_pages"
	reconcileReasonISBN            = "missing_isbn"
	reconcileReasonCatalogueFilter = "catalogue_filter"
)

// CatalogueReconciliationCandidate is a local, metadata-only Wanted row that
// the current primary-provider catalogue and metadata profile no longer
// accept. It contains no file operation: reconciliation deletes database rows
// only, and only after an explicit apply request.
type CatalogueReconciliationCandidate struct {
	BookID           int64  `json:"bookId"`
	Title            string `json:"title"`
	MetadataProvider string `json:"metadataProvider"`
	Reason           string `json:"reason"`
}

type CatalogueReconciliationSummary struct {
	Total             int            `json:"total"`
	Candidates        int            `json:"candidates"`
	Kept              int            `json:"kept"`
	Protected         int            `json:"protected"`
	ProtectedFiles    int            `json:"protectedFiles"`
	ProtectedImported int            `json:"protectedImported"`
	ProtectedStatus   int            `json:"protectedStatus"`
	ProtectedExcluded int            `json:"protectedExcluded"`
	Indeterminate     int            `json:"indeterminate"`
	Reasons           map[string]int `json:"reasons"`
}

type CatalogueReconciliationApplySummary struct {
	Requested int   `json:"requested"`
	Deleted   int64 `json:"deleted"`
	Skipped   int64 `json:"skipped"`
}

// CatalogueReconciliation is both the preview payload and, after apply, the
// deletion report. ProviderComplete is false when an upstream provider served
// only a partial author catalogue; in that case absence is never used as a
// deletion reason.
type CatalogueReconciliation struct {
	AuthorID         int64                                `json:"authorId"`
	AuthorName       string                               `json:"authorName"`
	Provider         string                               `json:"provider"`
	ProviderComplete bool                                 `json:"providerComplete"`
	ProfileName      string                               `json:"profileName"`
	Warning          string                               `json:"warning,omitempty"`
	Candidates       []CatalogueReconciliationCandidate   `json:"candidates"`
	Summary          CatalogueReconciliationSummary       `json:"summary"`
	Applied          *CatalogueReconciliationApplySummary `json:"applied,omitempty"`
}

type applyCatalogueReconciliationRequest struct {
	BookIDs []int64 `json:"bookIds"`
}

// PreviewCatalogueReconciliation handles
// GET /api/v1/author/{id}/catalogue-reconciliation. It is read-only: the
// provider is queried afresh and every candidate is reported with a reason.
func (h *AuthorHandler) PreviewCatalogueReconciliation(w http.ResponseWriter, r *http.Request) {
	author, ok := h.ownedAuthorFromRequest(w, r)
	if !ok {
		return
	}
	preview, err := h.buildCatalogueReconciliation(r.Context(), author)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// ApplyCatalogueReconciliation handles
// POST /api/v1/author/{id}/catalogue-reconciliation. It recomputes the preview
// and intersects it with the requested IDs before issuing one guarded DELETE;
// the client never gets to turn an arbitrary book ID into a deletion.
func (h *AuthorHandler) ApplyCatalogueReconciliation(w http.ResponseWriter, r *http.Request) {
	author, ok := h.ownedAuthorFromRequest(w, r)
	if !ok {
		return
	}
	var req applyCatalogueReconciliationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.BookIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bookIds is required"})
		return
	}

	preview, err := h.buildCatalogueReconciliation(r.Context(), author)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	current := make(map[int64]struct{}, len(preview.Candidates))
	for _, candidate := range preview.Candidates {
		current[candidate.BookID] = struct{}{}
	}
	requested := make(map[int64]struct{}, len(req.BookIDs))
	eligible := make([]int64, 0, len(req.BookIDs))
	for _, id := range req.BookIDs {
		if id <= 0 {
			continue
		}
		if _, duplicate := requested[id]; duplicate {
			continue
		}
		requested[id] = struct{}{}
		if _, stillCandidate := current[id]; stillCandidate {
			eligible = append(eligible, id)
		}
	}
	deleted, err := h.books.DeleteMetadataOnlyWantedByIDs(r.Context(), author.ID, eligible)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	preview.Applied = &CatalogueReconciliationApplySummary{
		Requested: len(requested),
		Deleted:   deleted,
		Skipped:   int64(len(requested)) - deleted,
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *AuthorHandler) ownedAuthorFromRequest(w http.ResponseWriter, r *http.Request) (*models.Author, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return nil, false
	}
	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return nil, false
	}
	if author == nil || !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return nil, false
	}
	return author, true
}

type reconciliationProfile struct {
	name            string
	allowedLangs    []string
	unknownFail     bool
	skipPartBooks   bool
	skipMissingDate bool
	minPopularity   int
	minPages        int
	skipMissingISBN bool
}

func (h *AuthorHandler) reconciliationProfile(ctx context.Context, author *models.Author) (reconciliationProfile, error) {
	if h.profiles == nil {
		return reconciliationProfile{}, errors.New("metadata profiles are not configured")
	}
	id := models.DefaultMetadataProfileID
	if author.MetadataProfileID != nil {
		id = *author.MetadataProfileID
	}
	profile, err := h.profiles.GetByID(ctx, id)
	if err != nil {
		return reconciliationProfile{}, fmt.Errorf("load metadata profile: %w", err)
	}
	if profile == nil {
		return reconciliationProfile{}, fmt.Errorf("metadata profile %d not found", id)
	}
	return reconciliationProfile{
		name:            profile.Name,
		allowedLangs:    models.ParseAllowedLanguages(profile.AllowedLanguages),
		unknownFail:     profile.UnknownLanguageBehavior == models.UnknownLanguageFail,
		skipPartBooks:   profile.SkipPartBooks,
		skipMissingDate: profile.SkipMissingDate,
		minPopularity:   profile.MinPopularity,
		minPages:        profile.MinPages,
		skipMissingISBN: profile.SkipMissingISBN,
	}, nil
}

type editionEvidence struct {
	editions []models.Edition
	known    bool
}

func (h *AuthorHandler) buildCatalogueReconciliation(ctx context.Context, author *models.Author) (CatalogueReconciliation, error) {
	if h.meta == nil {
		return CatalogueReconciliation{}, errors.New("metadata aggregator is not configured")
	}
	profile, err := h.reconciliationProfile(ctx, author)
	if err != nil {
		return CatalogueReconciliation{}, err
	}
	snapshot, err := h.meta.GetAuthorWorksSnapshotForAuthor(ctx, *author)
	if err != nil {
		return CatalogueReconciliation{}, fmt.Errorf("fetch current author catalogue: %w", err)
	}
	works := snapshot.Books
	if len(profile.allowedLangs) > 0 {
		// Best effort. A still-unknown language is always protected below, even
		// when the normal ingestion policy says unknown=fail: reconciliation
		// cannot distinguish missing metadata from a partial lookup failure.
		h.meta.FillMissingAuthorWorkLanguages(ctx, works)
	}

	editions := make(map[string]editionEvidence)
	if profile.minPages > 0 || profile.skipMissingISBN {
		var mu sync.Mutex
		concurrency.RunBounded(ctx, works, authorAutoSearchConcurrency, func(ctx context.Context, work models.Book) {
			if strings.TrimSpace(work.ForeignID) == "" {
				return
			}
			provider := bookProvider(&work)
			if provider == "dnb" {
				return
			}
			found, err := h.meta.GetEditionsFromProvider(ctx, provider, work.ForeignID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				editions[work.ForeignID] = editionEvidence{}
				return
			}
			editions[work.ForeignID] = editionEvidence{editions: found, known: true}
		})
	}

	acceptedIDs := make(map[string]struct{}, len(works))
	indeterminateIDs := make(map[string]struct{})
	rejectedIDs := make(map[string]string, len(works))
	acceptedTitles := indexer.NewTitleIndex[struct{}]()
	indeterminateTitles := indexer.NewTitleIndex[struct{}]()
	rejectedTitles := make(map[string]string)
	normalizedAuthor := strings.ToLower(strings.TrimSpace(author.Name))
	for _, work := range works {
		reason, indeterminate := reconciliationRejectReason(work, normalizedAuthor, profile, editions[work.ForeignID])
		if reason == "" {
			if strings.TrimSpace(work.ForeignID) != "" {
				acceptedIDs[work.ForeignID] = struct{}{}
			}
			acceptedTitles.Add(work.Title, struct{}{})
			if indeterminate {
				if strings.TrimSpace(work.ForeignID) != "" {
					indeterminateIDs[work.ForeignID] = struct{}{}
				}
				indeterminateTitles.Add(work.Title, struct{}{})
			}
			continue
		}
		if strings.TrimSpace(work.ForeignID) != "" {
			rejectedIDs[work.ForeignID] = reason
		}
		if key := indexer.CanonicalDedupKey(work.Title); key != "" {
			rejectedTitles[key] = reason
		}
	}

	localBooks, err := h.books.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		return CatalogueReconciliation{}, fmt.Errorf("list local author catalogue: %w", err)
	}
	identifiersByBookID, err := h.books.ListBookIdentifiersByAuthor(ctx, author.ID)
	if err != nil {
		return CatalogueReconciliation{}, fmt.Errorf("list identifiers for author %d: %w", author.ID, err)
	}
	result := CatalogueReconciliation{
		AuthorID:         author.ID,
		AuthorName:       author.Name,
		Provider:         snapshot.Provider,
		ProviderComplete: snapshot.Complete,
		ProfileName:      profile.name,
		Candidates:       []CatalogueReconciliationCandidate{},
		Summary: CatalogueReconciliationSummary{
			Total:   len(localBooks),
			Reasons: map[string]int{},
		},
	}
	if !snapshot.Complete {
		result.Warning = "The provider returned a partial catalogue. Missing works are protected; only explicit profile rejections can be removed."
	}

	for i := range localBooks {
		book := &localBooks[i]
		if !auth.CheckOwnership(ctx, book.OwnerUserID) {
			result.Summary.Protected++
			result.Summary.ProtectedStatus++
			continue
		}
		if book.FilePath != "" || book.EbookFilePath != "" || book.AudiobookFilePath != "" {
			result.Summary.Protected++
			result.Summary.ProtectedFiles++
			continue
		}
		if book.Status == models.BookStatusImported {
			result.Summary.Protected++
			result.Summary.ProtectedImported++
			continue
		}
		if book.Status != models.BookStatusWanted {
			result.Summary.Protected++
			result.Summary.ProtectedStatus++
			continue
		}
		if book.Excluded {
			result.Summary.Protected++
			result.Summary.ProtectedExcluded++
			continue
		}

		identifiers := identifiersByBookID[book.ID]
		ids := make([]string, 0, len(identifiers)+1)
		if strings.TrimSpace(book.ForeignID) != "" {
			ids = append(ids, book.ForeignID)
		}
		for _, identifier := range identifiers {
			if strings.TrimSpace(identifier.ForeignID) != "" {
				ids = append(ids, identifier.ForeignID)
			}
		}
		if anyReconciliationIDAccepted(ids, acceptedIDs) {
			result.Summary.Kept++
			if anyReconciliationIDAccepted(ids, indeterminateIDs) {
				result.Summary.Indeterminate++
			}
			continue
		}
		if _, accepted := acceptedTitles.Lookup(book.Title); accepted {
			result.Summary.Kept++
			if _, indeterminate := indeterminateTitles.Lookup(book.Title); indeterminate {
				result.Summary.Indeterminate++
			}
			continue
		}

		reason := reconciliationReasonForIDs(ids, rejectedIDs)
		if reason == "" {
			if titleReason, rejected := rejectedTitles[indexer.CanonicalDedupKey(book.Title)]; rejected {
				reason = titleReason
			}
		}
		if reason == "" {
			if !snapshot.Complete {
				result.Summary.Kept++
				result.Summary.Indeterminate++
				continue
			}
			if bookProvider(book) != snapshot.Provider {
				reason = reconcileReasonProviderChanged
			} else {
				reason = reconcileReasonNotInCatalogue
			}
		}

		result.Candidates = append(result.Candidates, CatalogueReconciliationCandidate{
			BookID:           book.ID,
			Title:            book.Title,
			MetadataProvider: bookProvider(book),
			Reason:           reason,
		})
		result.Summary.Reasons[reason]++
	}

	sort.Slice(result.Candidates, func(i, j int) bool {
		return strings.ToLower(result.Candidates[i].Title) < strings.ToLower(result.Candidates[j].Title)
	})
	result.Summary.Candidates = len(result.Candidates)
	return result, nil
}

func reconciliationRejectReason(work models.Book, normalizedAuthor string, profile reconciliationProfile, evidence editionEvidence) (string, bool) {
	normalizedTitle := strings.ToLower(strings.TrimSpace(work.Title))
	if normalizedTitle == "" || normalizedTitle == normalizedAuthor || work.IsCompilation || metadata.IsUnambiguousBundleTitle(work.Title) {
		return reconcileReasonCatalogueFilter, false
	}
	if len(profile.allowedLangs) > 0 {
		if strings.TrimSpace(work.Language) == "" {
			return "", profile.unknownFail
		}
		if !models.IsLanguageAllowed(work.Language, profile.allowedLangs, profile.unknownFail) {
			return reconcileReasonLanguage, false
		}
	}
	if profile.skipPartBooks && isPartBookTitle(work.Title) {
		return reconcileReasonPartBook, false
	}
	if profile.skipMissingDate && work.ReleaseDate == nil {
		return reconcileReasonMissingDate, false
	}
	hasRatingSignal := work.RatingsCount > 0 || work.AverageRating > 0
	if profile.minPopularity > 0 && hasRatingSignal && work.RatingsCount < profile.minPopularity &&
		(work.ReleaseDate == nil || !work.ReleaseDate.After(time.Now())) {
		return reconcileReasonPopularity, false
	}
	if profile.minPages > 0 || profile.skipMissingISBN {
		if !evidence.known {
			return "", true
		}
		if profile.skipMissingISBN && !anyEditionHasISBN(evidence.editions) {
			return reconcileReasonISBN, false
		}
		if profile.minPages > 0 && !passesMinPagesFilter(evidence.editions, profile.minPages) {
			return reconcileReasonPages, false
		}
	}
	return "", false
}

func anyReconciliationIDAccepted(ids []string, accepted map[string]struct{}) bool {
	for _, id := range ids {
		if _, ok := accepted[id]; ok {
			return true
		}
	}
	return false
}

func reconciliationReasonForIDs(ids []string, rejected map[string]string) string {
	for _, id := range ids {
		if reason := rejected[id]; reason != "" {
			return reason
		}
	}
	return ""
}

func bookProvider(book *models.Book) string {
	if provider := strings.ToLower(strings.TrimSpace(book.MetadataProvider)); provider != "" {
		return provider
	}
	return models.BookProviderFromForeignID(book.ForeignID)
}
