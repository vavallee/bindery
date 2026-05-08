package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
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
}

// WithFinder attaches a LibraryFinder so that Add can check whether the
// recommended book already exists on disk before queuing an auto-search.
func (h *RecommendationHandler) WithFinder(series *db.SeriesRepo, finder LibraryFinder) *RecommendationHandler {
	h.series = series
	h.finder = finder
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

	recs, err := h.recs.List(r.Context(), 1, recType, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

	if err := h.recs.Dismiss(r.Context(), 1, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found"})
		return
	}

	// Check if book already exists.
	existing, _ := h.books.GetByForeignID(r.Context(), rec.ForeignID)
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "book already exists"})
		return
	}

	// Resolve or create the author.
	var authorID int64
	if rec.AuthorID != nil {
		authorID = *rec.AuthorID
	} else if rec.AuthorName != "" {
		// Try to find an existing author by name.
		authors, _ := h.authors.List(r.Context())
		for _, a := range authors {
			if a.Name == rec.AuthorName {
				authorID = a.ID
				break
			}
		}
	}

	if authorID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot resolve author for this recommendation"})
		return
	}

	book := &models.Book{
		ForeignID:     rec.ForeignID,
		AuthorID:      authorID,
		Title:         rec.Title,
		Description:   rec.Description,
		ImageURL:      rec.ImageURL,
		AverageRating: rec.Rating,
		RatingsCount:  rec.RatingsCount,
		ReleaseDate:   rec.ReleaseDate,
		Language:      rec.Language,
		MediaType:     rec.MediaType,
		Status:        models.BookStatusWanted,
		Monitored:     true,
	}

	if err := h.books.Create(r.Context(), book); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	fileFound := handleNewWantedBook(r.Context(), h.books, h.series, h.finder, *book, rec.AuthorName)

	// Dismiss the recommendation now that the book is added.
	_ = h.recs.Dismiss(r.Context(), 1, id)

	// Trigger search in background unless the file already exists on disk.
	if h.searcher != nil && !fileFound {
		go h.searcher.SearchAndGrabBook(context.Background(), *book) // #nosec G118 -- intentional: search must outlive the request
	}

	writeJSON(w, http.StatusCreated, book)
}

// Refresh triggers a recommendation engine run in the background.
func (h *RecommendationHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := h.engine.Run(context.Background(), 1); err != nil {
			slog.Error("recommendation refresh failed", "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "refresh started"})
}

// ClearDismissals removes all dismissals for the current user.
func (h *RecommendationHandler) ClearDismissals(w http.ResponseWriter, r *http.Request) {
	if err := h.recs.ClearDismissals(r.Context(), 1); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListAuthorExclusions returns all excluded authors for the current user.
func (h *RecommendationHandler) ListAuthorExclusions(w http.ResponseWriter, r *http.Request) {
	exclusions, err := h.recs.ListAuthorExclusions(r.Context(), 1)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

	if err := h.recs.AddAuthorExclusion(r.Context(), 1, req.AuthorName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

	if err := h.recs.RemoveAuthorExclusion(r.Context(), 1, name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
