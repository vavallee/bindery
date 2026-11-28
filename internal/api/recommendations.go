package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// RecommendationEngine is the interface for triggering a recommendation run.
type RecommendationEngine interface {
	Run(ctx context.Context, userID int64) error
}

// RecommendationHandler handles the /api/v1/recommendations endpoints.
type RecommendationHandler struct {
	recs     *db.RecommendationRepo
	engine   RecommendationEngine
	authors  *db.AuthorRepo
	books    *db.BookRepo
	series   *db.SeriesRepo
	searcher BookSearcher
	finder   LibraryFinder
	editions *db.EditionRepo
	meta     *metadata.Aggregator

	editionFetcher bookhydrate.EditionFetcher

	// appCtx is the process-lifecycle context: not tied to any single HTTP
	// request but cancelled when the process shuts down. Background work
	// kicked off from a handler (which must outlive the request) will derive
	// from it instead of context.Background() — see #550. Never nil.
	appCtx context.Context
}

// WithFinder attaches a LibraryFinder so that Add can check whether the
// recommended book already exists on disk before queuing an auto-search.
func (h *RecommendationHandler) WithFinder(series *db.SeriesRepo, finder LibraryFinder) *RecommendationHandler {
	h.series = series
	h.finder = finder
	return h
}

// WithEditionHydration wires edition persistence for Hardcover
// recommendations accepted into the wanted list.
func (h *RecommendationHandler) WithEditionHydration(editions *db.EditionRepo, meta *metadata.Aggregator) *RecommendationHandler {
	h.editions = editions
	h.meta = meta
	return h
}

// WithEditionFetcher overrides the edition fetcher used by tests.
func (h *RecommendationHandler) WithEditionFetcher(fetcher bookhydrate.EditionFetcher) *RecommendationHandler {
	h.editionFetcher = fetcher
	return h
}

// hydrateHardcoverEditions fills the book's editions from Hardcover.
// mediaTypePinned forwards the caller's "this format was chosen, not guessed"
// signal so hydration leaves the media type alone (#2768).
func (h *RecommendationHandler) hydrateHardcoverEditions(ctx context.Context, book *models.Book, mediaTypePinned bool) {
	if book == nil || h.editions == nil {
		return
	}
	fetcher := h.editionFetcher
	if fetcher == nil && h.meta != nil {
		fetcher = func(ctx context.Context, foreignID string) ([]models.Edition, error) {
			return h.meta.GetEditionsFromProvider(ctx, "hardcover", foreignID)
		}
	}
	bookhydrate.HydrateHardcoverEditions(ctx, bookhydrate.Options{
		Book:            book,
		Provider:        book.MetadataProvider,
		Editions:        h.editions,
		Books:           h.books,
		FetchEditions:   fetcher,
		Enricher:        h.meta,
		MediaTypePinned: mediaTypePinned,
	})
}

// WithAppContext attaches the process-lifecycle context so that background
// work started from a request handler can be tied to process shutdown rather
// than to context.Background(). A nil ctx is tolerated and ignored. See #550.
func (h *RecommendationHandler) WithAppContext(ctx context.Context) *RecommendationHandler {
	if ctx != nil {
		h.appCtx = ctx
	}
	return h
}

// NewRecommendationHandler creates a new RecommendationHandler.
func NewRecommendationHandler(
	recs *db.RecommendationRepo,
	engine RecommendationEngine,
	authors *db.AuthorRepo,
	books *db.BookRepo,
	searcher BookSearcher,
) *RecommendationHandler {
	return &RecommendationHandler{
		recs:     recs,
		engine:   engine,
		authors:  authors,
		books:    books,
		searcher: searcher,
		appCtx:   context.Background(),
	}
}

// List returns non-dismissed recommendations for the current user.
func (h *RecommendationHandler) List(w http.ResponseWriter, r *http.Request) {
	recType := r.URL.Query().Get("type")

	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	recs, err := h.recs.List(r.Context(), auth.UserIDFromContext(r.Context()), recType, limit, offset)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if recs == nil {
		recs = []models.Recommendation{}
	}
	writeJSON(w, http.StatusOK, recs)
}

// Dismiss marks a recommendation as dismissed.
func (h *RecommendationHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Tier-1 cross-user IDOR guard (D1). Recommendations carry a UserID
	// column (see migration 015); fetch the row and verify ownership before
	// recording the dismissal so a non-owner cannot purge someone else's
	// feed by ID. 404 (not 403) on mismatch to avoid leaking existence.
	rec, err := h.recs.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if rec == nil || !auth.CheckOwnership(r.Context(), rec.UserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found"})
		return
	}

	if err := h.recs.Dismiss(r.Context(), auth.UserIDFromContext(r.Context()), id); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pickAuthorByName picks the author a recommendation is filed under when it
// carries only an author name. An admin sees every account's authors, so two
// accounts can each hold an author with that name, and the book takes the
// picked author's owner (#4090). When the matches belong to more than one
// owner, a shared (unowned) match wins; with no shared match the first one is
// used and ambiguous is true, so the caller leaves the book unowned instead of
// guessing which account it belongs to. With every match under one owner the
// first match is used, as before.
func pickAuthorByName(authors []models.Author, name string) (picked *models.Author, ambiguous bool) {
	var first, shared *models.Author
	owners := map[int64]bool{}
	for i := range authors {
		if authors[i].Name != name {
			continue
		}
		if first == nil {
			first = &authors[i]
		}
		if shared == nil && authors[i].OwnerUserID == 0 {
			shared = &authors[i]
		}
		owners[authors[i].OwnerUserID] = true
	}
	switch {
	case first == nil || len(owners) == 1:
		return first, false
	case shared != nil:
		return shared, false
	default:
		return first, true
	}
}

// Add creates an author (if needed) and book from a recommendation, marks it
// as wanted, and triggers an immediate search.
func (h *RecommendationHandler) Add(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	rec, err := h.recs.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), rec.UserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found"})
		return
	}

	// Check if book already exists.
	existing, _ := h.books.GetByForeignID(r.Context(), rec.ForeignID)
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "book already exists"})
		return
	}

	// Resolve the author among the ones the caller can see. Under tenancy the
	// recommendation's author id, or a name match, can point at another
	// user's author, and filing the new wanted book under it would put it in
	// that user's library and download into it.
	var author *models.Author
	if rec.AuthorID != nil {
		if a, _ := h.authors.GetByID(r.Context(), *rec.AuthorID); a != nil && auth.CheckOwnership(r.Context(), a.OwnerUserID) {
			author = a
		}
	}
	// ambiguousOwner is set when the name match below found authors with that
	// name in more than one account and none of them shared: the book is then
	// left unowned rather than put in whichever account's author sorted first.
	ambiguousOwner := false
	if author == nil && rec.AuthorName != "" {
		// Try to find an existing author by name.
		authors, _ := h.authors.ListByUser(r.Context(), auth.ListScopeUserID(r.Context()))
		author, ambiguousOwner = pickAuthorByName(authors, rec.AuthorName)
	}

	if author == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot resolve author for this recommendation"})
		return
	}

	// The owner Add Book gives the same work under the same author (#4090):
	// the author's owner, or the caller's library scope under a shared
	// author. A user who is not an admin owns what they add, and an admin's
	// book under their own author is theirs rather than shared with every
	// user.
	owner := newBookOwnerUserID(r.Context(), author)
	if ambiguousOwner {
		owner = 0
	}
	book := &models.Book{
		ForeignID:        rec.ForeignID,
		AuthorID:         author.ID,
		OwnerUserID:      owner,
		Title:            rec.Title,
		Description:      rec.Description,
		ImageURL:         rec.ImageURL,
		AverageRating:    rec.Rating,
		RatingsCount:     rec.RatingsCount,
		ReleaseDate:      rec.ReleaseDate,
		Language:         rec.Language,
		MediaType:        rec.MediaType,
		Status:           models.BookStatusWanted,
		Monitored:        true,
		MetadataProvider: metadataProviderFromForeignID(rec.ForeignID),
	}

	if err := h.books.Create(r.Context(), book); err != nil {
		writeServerError(w, r, err)
		return
	}
	// A recommendation's format is not a user choice: the add request carries
	// no body, and the recommender fills MediaType from the provider or falls
	// back to ebook. Under the AddBook rule a provider or default derived
	// format is not a pin, so hydration may widen it when Hardcover lists an
	// audio edition.
	h.hydrateHardcoverEditions(r.Context(), book, false)

	fileFound := handleNewWantedBook(r.Context(), h.books, h.series, h.finder, *book, rec.AuthorName)

	// Dismiss the recommendation now that the book is added.
	_ = h.recs.Dismiss(r.Context(), auth.UserIDFromContext(r.Context()), id)

	// Trigger search in background unless the file already exists on disk.
	// Use the process-lifecycle context so the goroutine is cancelled on
	// shutdown rather than running forever on context.Background(). See #550.
	if h.searcher != nil && !fileFound {
		go h.searcher.SearchAndGrabBook(indexer.WithSearchOrigin(h.appCtx, indexer.OriginRecommendation), *book) // #nosec G118 -- intentional: search must outlive the request
	}

	writeBook(w, http.StatusCreated, book)
}

// Refresh triggers a recommendation engine run in the background.
// The goroutine derives its context from the process-lifecycle context so it
// is cancelled on shutdown instead of running on context.Background(). See #550.
func (h *RecommendationHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	// Capture the caller's user id before the goroutine: r may be recycled
	// once this handler returns, so the background run must not read from it.
	uid := auth.UserIDFromContext(r.Context())
	go func() {
		if err := h.engine.Run(h.appCtx, uid); err != nil {
			slog.Error("recommendation refresh failed", "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "refresh started"})
}

// ClearDismissals removes all dismissals for the current user.
func (h *RecommendationHandler) ClearDismissals(w http.ResponseWriter, r *http.Request) {
	if err := h.recs.ClearDismissals(r.Context(), auth.UserIDFromContext(r.Context())); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListAuthorExclusions returns all excluded authors for the current user.
func (h *RecommendationHandler) ListAuthorExclusions(w http.ResponseWriter, r *http.Request) {
	exclusions, err := h.recs.ListAuthorExclusions(r.Context(), auth.UserIDFromContext(r.Context()))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if exclusions == nil {
		exclusions = []string{}
	}
	writeJSON(w, http.StatusOK, exclusions)
}

// ExcludeAuthor adds an author to the exclusion list.
func (h *RecommendationHandler) ExcludeAuthor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AuthorName string `json:"authorName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AuthorName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorName required"})
		return
	}

	if err := h.recs.AddAuthorExclusion(r.Context(), auth.UserIDFromContext(r.Context()), req.AuthorName); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// RemoveAuthorExclusion removes an author from the exclusion list.
func (h *RecommendationHandler) RemoveAuthorExclusion(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "author name required"})
		return
	}

	if err := h.recs.RemoveAuthorExclusion(r.Context(), auth.UserIDFromContext(r.Context()), name); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
