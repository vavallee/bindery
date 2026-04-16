package api

import (
	"encoding/xml"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/opds"
)

// OPDSHandler exposes the `/opds/*` routes. It owns a builder (assembles
// Atom/OPDS feeds) and re-uses the existing FileHandler to actually serve
// book bytes — so audiobook zip-streaming and ebook Content-Disposition
// logic stay in one place.
type OPDSHandler struct {
	builder *opds.Builder
	files   *FileHandler
	books   *db.BookRepo
}

// NewOPDSHandler wires the builder + file serving paths together.
func NewOPDSHandler(builder *opds.Builder, books *db.BookRepo, files *FileHandler) *OPDSHandler {
	return &OPDSHandler{builder: builder, files: files, books: books}
}

// Root serves the navigation feed at GET /opds.
func (h *OPDSHandler) Root(w http.ResponseWriter, r *http.Request) {
	feed := h.builder.BuildRoot(baseURL(r))
	writeOPDS(w, feed)
}

// Authors serves the paginated author navigation feed.
func (h *OPDSHandler) Authors(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	feed, err := h.builder.BuildAuthors(r.Context(), baseURL(r), page)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeOPDS(w, feed)
}

// Author serves an acquisition feed for a single author's imported books.
func (h *OPDSHandler) Author(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	feed, err := h.builder.BuildAuthor(r.Context(), baseURL(r), id)
	if writeOPDSError(w, err) {
		return
	}
	writeOPDS(w, feed)
}

// Series serves the paginated series navigation feed.
func (h *OPDSHandler) Series(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	feed, err := h.builder.BuildSeriesList(r.Context(), baseURL(r), page)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeOPDS(w, feed)
}

// OneSeries serves an acquisition feed for a single series' imported books.
func (h *OPDSHandler) OneSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	feed, err := h.builder.BuildSeries(r.Context(), baseURL(r), id)
	if writeOPDSError(w, err) {
		return
	}
	writeOPDS(w, feed)
}

// Recent serves an acquisition feed of the last-50-imported books.
func (h *OPDSHandler) Recent(w http.ResponseWriter, r *http.Request) {
	feed, err := h.builder.BuildRecent(r.Context(), baseURL(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeOPDS(w, feed)
}

// Book serves a single-entry acquisition feed for one book — the detail
// view some clients open before triggering the download link.
func (h *OPDSHandler) Book(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	feed, err := h.builder.BuildBook(r.Context(), baseURL(r), id)
	if writeOPDSError(w, err) {
		return
	}
	writeOPDS(w, feed)
}

// DownloadFile serves the book bytes. Delegates to the same FileHandler
// used by the main API so audiobook directories turn into zips and ebook
// extensions drive the Content-Disposition filename.
func (h *OPDSHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	// The file handler pulls the book id out of chi.URLParam("id"), which
	// matches our route shape (`/opds/book/{id}/file`), so no adaptation
	// is needed.
	h.files.Download(w, r)
}

// --- helpers ----------------------------------------------------------------

// baseURL reconstructs the absolute URL prefix the client used to reach us
// (scheme + host), honouring X-Forwarded-* when we're behind Traefik. OPDS
// feeds embed fully-qualified links, so the value here flows into every
// <link href="..."/>.
func baseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

// writeOPDS encodes the feed and writes it with the canonical OPDS
// Content-Type.
func writeOPDS(w http.ResponseWriter, feed opds.Feed) {
	w.Header().Set("Content-Type", opds.ContentTypeFeed)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		slog.Warn("failed to write OPDS header", "error", err)
		return
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(feed); err != nil {
		slog.Warn("failed to encode OPDS feed", "error", err)
	}
}

// writeOPDSError handles known builder errors (currently just ErrNotFound).
// Returns true when it wrote a response so the caller can bail out.
func writeOPDSError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, opds.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return true
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	return true
}
