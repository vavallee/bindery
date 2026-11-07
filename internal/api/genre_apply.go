package api

import (
	"context"
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

// writableBooks returns the books auth.CheckWriteOwnership lets the caller
// change, in order. A fresh slice is returned; the input is left alone. With
// tenancy off, or for an admin, every book is kept.
func writableBooks(ctx context.Context, books []models.Book) []models.Book {
	out := make([]models.Book, 0, len(books))
	for i := range books {
		if auth.CheckWriteOwnership(ctx, books[i].OwnerUserID) {
			out = append(out, books[i])
		}
	}
	return out
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
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckWriteOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	var req genreApplyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	books, err := h.books.ListByAuthor(r.Context(), author.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// Only the books the caller may write: a shared author can hold other
	// accounts' books and unowned ones.
	updated, err := applyGenresToBooks(r.Context(), h.books, writableBooks(r.Context(), books), cleanGenreList(req.Genres))
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
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if series == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	var req genreApplyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	members, err := h.series.ListBooksInSeries(r.Context(), series.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// ListBooksInSeries is a partial projection, and BookRepo.Update writes
	// every column, so the write goes through the full stored rows. Otherwise
	// each book in the series loses its description, file paths, other locks
	// and every column the projection leaves out.
	ids := make([]int64, len(members))
	for i := range members {
		ids[i] = members[i].ID
	}
	full, err := h.books.GetByIDs(r.Context(), ids)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	books := make([]models.Book, 0, len(members))
	for _, id := range ids {
		if b, ok := full[id]; ok {
			books = append(books, *b)
		}
	}
	genres := cleanGenreList(req.Genres)
	if err := h.series.SetGenreOverride(r.Context(), series.ID, genres); err != nil {
		writeServerError(w, r, err)
		return
	}
	// The route is admin only, so this keeps every book today; it stays as the
	// per book write gate in case the route is ever opened to users.
	updated, err := applyGenresToBooks(r.Context(), h.books, writableBooks(r.Context(), books), genres)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}

// ClearGenres handles DELETE /series/{id}/genres — drop the series genre
// override so books added later take their genres from metadata again (#1709).
//
// Deliberately asymmetric with ApplyGenres: this clears the *policy* for future
// books and leaves the genres already written onto existing books alone. Those
// were an explicit edit and stay locked; unlocking them here would silently
// discard a user's taxonomy on the next metadata refresh, which is the exact
// data loss the override exists to prevent.
func (h *SeriesHandler) ClearGenres(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	series, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if series == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	if err := h.series.ClearGenreOverride(r.Context(), series.ID); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}
