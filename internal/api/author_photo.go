package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// maxAuthorDescriptionBytes caps a bio typed in Edit Author. A Wikipedia
// lead summary is a few hundred bytes; this leaves room for a long one
// without letting the field hold a document.
const maxAuthorDescriptionBytes = 20000

// WithCoverStore enables POST /author/{id}/photo. Without a store the endpoint
// answers 503.
func (h *AuthorHandler) WithCoverStore(s *covers.Store) *AuthorHandler {
	h.coverStore = s
	return h
}

// UploadPhoto sets an author's photo from the image in the request body, the
// author counterpart of POST /book/{id}/cover (#3017, #3287). The bytes go into
// the same cover store, sniffed and capped like every stored cover, so the
// photo no longer depends on any remote host.
//
// Unlike a book cover, an author photo is replaced by a profile refresh when
// the provider has one, so the upload is marked as the user's own
// (models.ProfileSourceUser): no refresh or enricher replaces it afterwards.
func (h *AuthorHandler) UploadPhoto(w http.ResponseWriter, r *http.Request) {
	author, ok := h.loadOwnedAuthor(w, r)
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
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "photo exceeds 10 MB"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the upload"})
		return
	}
	ref, err := h.coverStore.PutBytes(body)
	switch {
	case errors.Is(err, covers.ErrTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "photo exceeds 10 MB"})
		return
	case errors.Is(err, covers.ErrNotImage):
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "send a JPEG, PNG, WebP or GIF image"})
		return
	case err != nil:
		writeServerError(w, r, err)
		return
	}
	author.ImageURL = ref
	author.ImageSource = &models.ProfileSource{Provider: models.ProfileSourceUser}
	if err := h.authors.Update(r.Context(), author); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeAuthor(w, http.StatusOK, author)
}
