package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// genreApplyRequest is the shared payload for the author- and series-level
// genre override (#1446): set the same genre list on every book under the
// author/series and lock the field so metadata refresh leaves it alone.
// An empty list is valid — it means "no genre, and stop refilling it".
type genreApplyRequest struct {
	Genres []string `json:"genres"`
}

// cleanGenreList trims entries and drops empties, preserving order.
func cleanGenreList(genres []string) []string {
	cleaned := make([]string, 0, len(genres))
	for _, g := range genres {
		if g = strings.TrimSpace(g); g != "" {
			cleaned = append(cleaned, g)
		}
	}
	return cleaned
}

// applyGenresToBooks sets + locks the genre list on each book and persists
// it. Returns how many rows were updated; the first persistence error aborts
// (partial application is reported via the count so the client can retry —
// the operation is idempotent).
func applyGenresToBooks(ctx context.Context, repo *db.BookRepo, books []models.Book, genres []string) (int, error) {
	updated := 0
	for i := range books {
		b := &books[i]
		b.Genres = genres
		b.LockField(models.BookFieldGenres)
		if err := repo.Update(ctx, b); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// ApplyGenres handles PUT /author/{id}/genres — the author-level genre
// override from #1446.
func (h *AuthorHandler) ApplyGenres(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil || author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	var req genreApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	books, err := h.books.ListByAuthor(r.Context(), author.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	updated, err := applyGenresToBooks(r.Context(), h.books, books, cleanGenreList(req.Genres))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}

// ApplyGenres handles PUT /series/{id}/genres — the series-level genre
// override from #1446.
func (h *SeriesHandler) ApplyGenres(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	series, err := h.series.GetByID(r.Context(), id)
	if err != nil || series == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	var req genreApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	books, err := h.series.ListBooksInSeries(r.Context(), series.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	updated, err := applyGenresToBooks(r.Context(), h.books, books, cleanGenreList(req.Genres))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}
