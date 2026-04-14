package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// BulkHandler handles multi-ID mutation endpoints for authors, books, and
// wanted books. All three endpoints use a per-ID result map with 200 status
// so callers can distinguish partial failures without a second request.
// Rationale for per-ID rather than all-or-nothing: bulk operations routinely
// include stale IDs (page loaded, item deleted, user re-selects); aborting
// the whole batch on one bad ID is more disruptive than reporting it inline.
type BulkHandler struct {
	authors   *db.AuthorRepo
	books     *db.BookRepo
	blocklist *db.BlocklistRepo
	searcher  BookSearcher
}

func NewBulkHandler(authors *db.AuthorRepo, books *db.BookRepo, blocklist *db.BlocklistRepo, searcher BookSearcher) *BulkHandler {
	return &BulkHandler{authors: authors, books: books, blocklist: blocklist, searcher: searcher}
}

// bulkItemResult is the per-ID result entry in a bulk response.
type bulkItemResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// bulkResponse is the envelope returned by all three bulk endpoints.
// Keys are stringified IDs; every requested ID has an entry.
type bulkResponse struct {
	Results map[string]bulkItemResult `json:"results"`
}

// AuthorsBulk handles POST /api/v1/author/bulk.
//
// Supported actions: "monitor", "unmonitor", "delete", "search".
// "search" fires an async indexer search for every wanted book belonging
// to each requested author and always returns ok:true immediately (the search
// outcome is visible in History).
func (h *BulkHandler) AuthorsBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
		return
	}

	switch req.Action {
	case "monitor", "unmonitor", "delete", "search":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
		return
	}

	resp := bulkResponse{Results: make(map[string]bulkItemResult, len(req.IDs))}

	for _, id := range req.IDs {
		key := fmt.Sprintf("%d", id)
		switch req.Action {
		case "monitor":
			if err := h.setAuthorMonitored(r.Context(), id, true); err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
		case "unmonitor":
			if err := h.setAuthorMonitored(r.Context(), id, false); err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
		case "delete":
			if err := h.authors.Delete(r.Context(), id); err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
		case "search":
			// Fetch wanted books while the request context is still alive,
			// then detach before launching per-book goroutines.
			books, err := h.books.ListByAuthor(r.Context(), id)
			if err != nil {
				resp.Results[key] = bulkItemResult{Error: err.Error()}
				continue
			}
			if h.searcher != nil {
				bgCtx := contextBackground()
				for _, b := range books {
					if b.Status == models.BookStatusWanted {
						b := b
						go h.searcher.SearchAndGrabBook(bgCtx, b)
					}
				}
			}
		}
		resp.Results[key] = bulkItemResult{OK: true}
	}

	writeJSON(w, http.StatusOK, resp)
}

// BooksBulk handles POST /api/v1/book/bulk.
//
// Supported actions: "monitor", "unmonitor", "delete", "search", "set_media_type".
// For "set_media_type" the body must also include "mediaType": "ebook"|"audiobook".
func (h *BulkHandler) BooksBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs       []int64 `json:"ids"`
		Action    string  `json:"action"`
		MediaType string  `json:"mediaType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
		return
	}

	switch req.Action {
	case "monitor", "unmonitor", "delete", "search", "set_media_type":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
		return
	}
	if req.Action == "set_media_type" && req.MediaType != models.MediaTypeEbook && req.MediaType != models.MediaTypeAudiobook {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mediaType must be 'ebook' or 'audiobook'"})
		return
	}

	resp := bulkResponse{Results: make(map[string]bulkItemResult, len(req.IDs))}

	for _, id := range req.IDs {
		key := fmt.Sprintf("%d", id)
		var opErr error
		switch req.Action {
		case "monitor":
			opErr = h.setBookMonitored(r.Context(), id, true)
		case "unmonitor":
			opErr = h.setBookMonitored(r.Context(), id, false)
		case "delete":
			opErr = h.books.Delete(r.Context(), id)
		case "search":
			book, err := h.books.GetByID(r.Context(), id)
			if err != nil || book == nil {
				resp.Results[key] = bulkItemResult{Error: "book not found"}
				continue
			}
			if h.searcher != nil {
				b := *book
				go h.searcher.SearchAndGrabBook(contextBackground(), b)
			}
		case "set_media_type":
			opErr = h.setBookMediaType(r.Context(), id, req.MediaType)
		}
		if opErr != nil {
			resp.Results[key] = bulkItemResult{Error: opErr.Error()}
			continue
		}
		resp.Results[key] = bulkItemResult{OK: true}
	}

	writeJSON(w, http.StatusOK, resp)
}

// WantedBulk handles POST /api/v1/wanted/bulk.
//
// Supported actions: "search", "unmonitor", "blocklist".
// "blocklist" marks the book as unmonitored with status "skipped" so it
// disappears from the Wanted page and is not auto-searched again.
func (h *BulkHandler) WantedBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
		return
	}

	switch req.Action {
	case "search", "unmonitor", "blocklist":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
		return
	}

	resp := bulkResponse{Results: make(map[string]bulkItemResult, len(req.IDs))}

	for _, id := range req.IDs {
		key := fmt.Sprintf("%d", id)
		var opErr error
		switch req.Action {
		case "search":
			book, err := h.books.GetByID(r.Context(), id)
			if err != nil || book == nil {
				resp.Results[key] = bulkItemResult{Error: "book not found"}
				continue
			}
			if h.searcher != nil {
				b := *book
				go h.searcher.SearchAndGrabBook(contextBackground(), b)
			}
		case "unmonitor":
			opErr = h.setBookMonitored(r.Context(), id, false)
		case "blocklist":
			opErr = h.skipBook(r.Context(), id)
		}
		if opErr != nil {
			resp.Results[key] = bulkItemResult{Error: opErr.Error()}
			continue
		}
		resp.Results[key] = bulkItemResult{OK: true}
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- helpers -----------------------------------------------------------------

func (h *BulkHandler) setAuthorMonitored(ctx context.Context, id int64, monitored bool) error {
	author, err := h.authors.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if author == nil {
		return fmt.Errorf("author not found")
	}
	author.Monitored = monitored
	return h.authors.Update(ctx, author)
}

func (h *BulkHandler) setBookMonitored(ctx context.Context, id int64, monitored bool) error {
	book, err := h.books.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if book == nil {
		return fmt.Errorf("book not found")
	}
	book.Monitored = monitored
	return h.books.Update(ctx, book)
}

func (h *BulkHandler) setBookMediaType(ctx context.Context, id int64, mediaType string) error {
	book, err := h.books.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if book == nil {
		return fmt.Errorf("book not found")
	}
	book.MediaType = mediaType
	return h.books.Update(ctx, book)
}

// skipBook marks a wanted book as unmonitored and skipped so it is removed
// from the Wanted list and not auto-searched again. Equivalent to the
// per-book Sonarr "mark as unmonitored" workflow when the user has no
// interest in acquiring the title.
func (h *BulkHandler) skipBook(ctx context.Context, id int64) error {
	book, err := h.books.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if book == nil {
		return fmt.Errorf("book not found")
	}
	book.Monitored = false
	book.Status = models.BookStatusSkipped
	return h.books.Update(ctx, book)
}
