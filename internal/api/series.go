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

type SeriesHandler struct {
	series   *db.SeriesRepo
	books    *db.BookRepo
	searcher BookSearcher
}

func NewSeriesHandler(series *db.SeriesRepo, books *db.BookRepo, searcher BookSearcher) *SeriesHandler {
	return &SeriesHandler{series: series, books: books, searcher: searcher}
}

func (h *SeriesHandler) List(w http.ResponseWriter, r *http.Request) {
	series, err := h.series.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if series == nil {
		series = []models.Series{}
	}
	writeJSON(w, http.StatusOK, series)
}

func (h *SeriesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	s, err := h.series.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
		return
	}
	if s.Books == nil {
		s.Books = []models.SeriesBook{}
	}
	writeJSON(w, http.StatusOK, s)
}

// Monitor toggles the monitored flag on a series.
func (h *SeriesHandler) Monitor(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var body struct {
		Monitored bool `json:"monitored"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := h.series.SetMonitored(r.Context(), id, body.Monitored); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"monitored": body.Monitored})
}

// Fill marks all non-imported books in a series as wanted and kicks off searches.
func (h *SeriesHandler) Fill(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	books, err := h.series.ListBooksInSeries(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	queued := 0
	for _, b := range books {
		if b.Status == models.BookStatusImported {
			continue
		}
		b.Status = models.BookStatusWanted
		b.Monitored = true
		if err := h.books.Update(r.Context(), &b); err != nil {
			slog.Warn("series fill: failed to update book", "book", b.Title, "error", err)
			continue
		}
		queued++
		go h.searcher.SearchAndGrabBook(context.Background(), b)
	}

	writeJSON(w, http.StatusOK, map[string]int{"queued": queued})
}
