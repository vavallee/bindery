package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/vavallee/bindery/internal/covers"
)

// WithCoverStore enables POST /book/{id}/cover. Without a store the endpoint
// answers 503.
func (h *BookHandler) WithCoverStore(s *covers.Store) *BookHandler {
	h.coverStore = s
	return h
}

// UploadCover sets a book's cover from the image in the request body: a
// browser upload from the book page, or a script supplying covers its owner
// sourced. The bytes go into Bindery's own cover store, sniffed and capped
// like every stored cover, so the book no longer depends on any remote host.
//
// The cover replaces whatever the book had. Author refresh only fills an
// empty cover (#1748), so it is not overwritten later.
func (h *BookHandler) UploadCover(w http.ResponseWriter, r *http.Request) {
	book, ok := h.loadWritableBook(w, r)
	if !ok {
		return
	}
	if h.coverStore == nil || h.coverStore.Dir() == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cover store not configured"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, covers.MaxBytes+1))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "cover exceeds 10 MB"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the upload"})
		return
	}
	ref, err := h.coverStore.PutBytes(body)
	switch {
	case errors.Is(err, covers.ErrTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "cover exceeds 10 MB"})
		return
	case errors.Is(err, covers.ErrNotImage):
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "send a JPEG, PNG, WebP or GIF image"})
		return
	case err != nil:
		writeServerError(w, r, err)
		return
	}
	if err := h.books.SetImageURL(r.Context(), book.ID, ref); err != nil {
		writeServerError(w, r, err)
		return
	}
	book.ImageURL = ref
	// The same shape GET /book/{id} answers with, files and identifiers
	// included since the page replaces its book with this: the stored
	// reference is rewritten to the image route a browser can load.
	h.prepareBookDetail(r.Context(), book)
	writeBook(w, http.StatusOK, book)
}
