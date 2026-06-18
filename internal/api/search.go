package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type SearchHandler struct {
	meta *metadata.Aggregator
}

func NewSearchHandler(meta *metadata.Aggregator) *SearchHandler {
	return &SearchHandler{meta: meta}
}

// writeUpstreamError responds with 502 Bad Gateway and a message that makes
// it obvious the failure is on the metadata provider side (OpenLibrary,
// Google Books, Hardcover), not inside Bindery. Using 500 for this conflates
// provider outages with real server bugs and trains users to ignore 500s.
//
// The client-facing message is intentionally generic and never includes the
// underlying err string. Transport errors from the metadata clients wrap a
// *url.Error whose Error() embeds the full upstream request URL, and the
// Google Books URL carries the API key (?key=...) in the query string plus the
// internal DNS resolver IP. Echoing err.Error() back to the caller therefore
// leaked the API key and internal infra (#1144). The full error is still
// logged server-side so operators keep the detail for debugging.
func writeUpstreamError(w http.ResponseWriter, err error) {
	slog.Warn("metadata provider request failed", "error", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{
		"error": "metadata provider unavailable",
	})
}

func (h *SearchHandler) SearchAuthors(w http.ResponseWriter, r *http.Request) {
	term := r.URL.Query().Get("term")
	if term == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "term parameter required"})
		return
	}

	authors, err := h.meta.SearchAuthors(r.Context(), term)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, authors)
}

func (h *SearchHandler) SearchBooks(w http.ResponseWriter, r *http.Request) {
	term := r.URL.Query().Get("term")
	if term == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "term parameter required"})
		return
	}

	books, err := h.meta.SearchBooks(r.Context(), term)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, books)
}

// Lookup resolves a single book by a stable identifier passed as a query
// param: either `isbn` (the original behavior) or `asin` (an Audible/audiobook
// identifier). The route is shared (`/book/lookup`) so existing ISBN callers
// keep working unchanged.
func (h *SearchHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	asin := strings.TrimSpace(r.URL.Query().Get("asin"))
	isbn := r.URL.Query().Get("isbn")

	switch {
	case asin != "":
		h.lookupByASIN(w, r, asin)
	case isbn != "":
		h.lookupByISBN(w, r, isbn)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "isbn or asin parameter required"})
	}
}

func (h *SearchHandler) lookupByISBN(w http.ResponseWriter, r *http.Request, isbn string) {
	book, err := h.meta.GetBookByISBN(r.Context(), isbn)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("No book found for ISBN %s. Check the number, or try searching by title instead.", isbn),
		})
		return
	}

	writeJSON(w, http.StatusOK, book)
}

func (h *SearchHandler) lookupByASIN(w http.ResponseWriter, r *http.Request, asin string) {
	book, err := h.meta.GetCanonicalBookByASIN(r.Context(), asin)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("No book found for ASIN %s. Check the identifier, or try searching by title instead.", asin),
		})
		return
	}

	// The resolver canonicalizes the ASIN against the primary provider, so the
	// returned book carries the canonical foreignBookId (keep it) but loses the
	// ASIN-origin shape. Re-stamp the ASIN and audiobook media type so the Add
	// Book modal renders it as the audiobook edition the user searched for.
	if book.ASIN == "" {
		book.ASIN = asin
	}
	book.MediaType = models.MediaTypeAudiobook

	writeJSON(w, http.StatusOK, book)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Warn("failed to encode JSON response", "status", status, "error", err)
	}
}
