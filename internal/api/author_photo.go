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
	if !h.saveAuthorIfUnchanged(w, r, author) {
		return
	}
	writeAuthor(w, http.StatusOK, author)
}

// ClearPhoto removes a photo the user uploaded and hands the field back to the
// provider and the enrichers (#3287): the photo is cleared, then the author's
// profile is refreshed as by its Refresh button, so a provider or Wikidata
// photo shows at once instead of after the next scheduled refresh. Only an
// uploaded photo can be cleared this way: a provider's photo would come back
// on that same refresh, so clearing it is refused with 409.
//
// The stored file stays. The cover store is content addressed and shared with
// book covers, so the same bytes may back a cover too, and a replaced book
// cover is not deleted either.
func (h *AuthorHandler) ClearPhoto(w http.ResponseWriter, r *http.Request) {
	author, ok := h.loadOwnedAuthor(w, r)
	if !ok {
		return
	}
	if author.ImageSource == nil || author.ImageSource.Provider != models.ProfileSourceUser {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "only a photo you uploaded can be removed", "code": codePhotoNotUploaded})
		return
	}
	author.ImageURL = ""
	author.ImageSource = nil
	if !h.saveAuthorIfUnchanged(w, r, author) {
		return
	}
	h.refreshAuthorProfile(r.Context(), author, true)
	writeAuthor(w, http.StatusOK, author)
}

// codePhotoNotUploaded marks ClearPhoto's 409 for a photo the user did not
// upload, so the page can tell it apart from errAuthorChanged's 409.
const codePhotoNotUploaded = "photo_not_uploaded"

// errAuthorChanged is the 409 a manual author save answers when the row
// changed since it was read, such as a scheduled profile refresh committing in
// between. The page shows it and the user saves again.
const errAuthorChanged = "this author changed while you were saving; reload and try again"

// saveAuthorIfUnchanged writes a manual edit only if the author row is as the
// handler read it, the same guard the scheduled refresh uses
// (persistRefreshedAuthor), so neither silently overwrites the other. It
// answers 409 or 500 itself and reports whether the save happened.
func (h *AuthorHandler) saveAuthorIfUnchanged(w http.ResponseWriter, r *http.Request, author *models.Author) bool {
	written, err := h.authors.UpdateIfUnchanged(r.Context(), author, author.UpdatedAtRaw)
	if err != nil {
		writeServerError(w, r, err)
		return false
	}
	if !written {
		writeJSON(w, http.StatusConflict, map[string]string{"error": errAuthorChanged})
		return false
	}
	return true
}
