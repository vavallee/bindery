package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
)

// UpdateEdition is PUT /book/{id}/edition/{editionId}: a user edit of an
// edition's publication fields. Changing a field applies the value and locks
// it against imports and hydration, the contract PUT /book/{id} has for the
// book's fields; lockedFields, when present, replaces the lock set. The
// edition is resolved through a book the caller may write, and one that is not
// this book's answers exactly as a missing book does, so the route reveals
// nothing about other users' catalogues.
func (h *BookHandler) UpdateEdition(w http.ResponseWriter, r *http.Request) {
	book, ok := h.loadWritableBook(w, r)
	if !ok {
		return
	}
	if h.editions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "edition repository unavailable"})
		return
	}
	editionID, err := strconv.ParseInt(chi.URLParam(r, "editionId"), 10, 64)
	if err != nil || editionID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid edition id"})
		return
	}
	edition, err := h.editions.GetByID(r.Context(), editionID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if edition == nil || edition.BookID != book.ID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}

	var req struct {
		Publisher    *string   `json:"publisher"`
		PublishDate  *string   `json:"publishDate"`
		ISBN13       *string   `json:"isbn13"`
		ISBN10       *string   `json:"isbn10"`
		ASIN         *string   `json:"asin"`
		LockedFields *[]string `json:"lockedFields"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	// A field locks only when the submitted value differs from the stored
	// one, the rule PUT /book/{id} follows: the body of a GET can be sent
	// back unchanged without locking anything, and lockedFields is how to
	// lock a field without changing it. Validation also runs only on a
	// change, so a stored value an import wrote is never refused.
	if req.Publisher != nil {
		v := strings.TrimSpace(*req.Publisher)
		if v != edition.Publisher {
			if len(v) > maxEditionPublisherBytes {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("publisher must be at most %d bytes", maxEditionPublisherBytes)})
				return
			}
			edition.Publisher = v
			edition.LockField(models.EditionFieldPublisher)
		}
	}
	if req.PublishDate != nil {
		d, ok := parseDateField(strings.TrimSpace(*req.PublishDate))
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "publishDate must be YYYY-MM-DD, an RFC 3339 timestamp, or empty"})
			return
		}
		if !sameDay(d, edition.PublishDate) {
			edition.PublishDate = d
			edition.LockField(models.EditionFieldPublishDate)
		}
	}
	if req.ISBN13 != nil {
		v := isbnutil.Normalize(*req.ISBN13)
		if v != derefString(edition.ISBN13) {
			if v != "" && !isbnutil.ValidISBN13(v) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "isbn13 is not a valid ISBN-13"})
				return
			}
			edition.ISBN13 = optionalString(v)
			edition.LockField(models.EditionFieldISBN13)
		}
	}
	if req.ISBN10 != nil {
		v := isbnutil.Normalize(*req.ISBN10)
		if v != derefString(edition.ISBN10) {
			if v != "" && !isbnutil.ValidISBN10(v) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "isbn10 is not a valid ISBN-10"})
				return
			}
			edition.ISBN10 = optionalString(v)
			edition.LockField(models.EditionFieldISBN10)
		}
	}
	if req.ASIN != nil {
		v := isbnutil.NormalizeASIN(*req.ASIN)
		if v != derefString(edition.ASIN) {
			if v != "" && !validASIN(v) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "asin must be 10 letters or digits"})
				return
			}
			edition.ASIN = optionalString(v)
			edition.LockField(models.EditionFieldASIN)
		}
	}
	if req.LockedFields != nil {
		cleaned := make([]string, 0, len(*req.LockedFields))
		for _, f := range *req.LockedFields {
			if !models.IsLockableEditionField(f) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown locked field %q (valid: %s)", f, strings.Join(models.LockableEditionFields, ", "))})
				return
			}
			if !slices.Contains(cleaned, f) {
				cleaned = append(cleaned, f)
			}
		}
		edition.LockedFields = cleaned
	}

	updated, err := h.editions.UpdateFields(r.Context(), edition, book.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !updated {
		// Either the row changed under the edit (an import or a second edit
		// landed between the read and the write) or it moved to another book
		// or vanished. A re-read tells the two apart; the second answers as a
		// missing book, the same as above.
		current, rerr := h.editions.GetByID(r.Context(), edition.ID)
		if rerr != nil {
			writeServerError(w, r, rerr)
			return
		}
		if current != nil && current.BookID == book.ID {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "This edition changed while it was being edited. Try again.", "reason": conflictReasonChanged})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	stored, err := h.editions.GetByID(r.Context(), edition.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if stored == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// stored is this request's own read, so rewriting its cover for the client
	// cannot reach the database.
	stored.ImageURL = ProxyImageURL(stored.ImageURL)
	writeJSON(w, http.StatusOK, stored)
}

// optionalString stores an empty value as NULL, the way the importers leave
// an unknown identifier.
func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
