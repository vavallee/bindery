package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// adoptRequest names the book a unit is, one of two ways: a book already in
// the library (bookId), or a metadata result to add (foreignBookId with its
// author). There is no path field; the unit's files come from its row.
type adoptRequest struct {
	BookID          int64  `json:"bookId"`
	ForeignBookID   string `json:"foreignBookId"`
	ForeignAuthorID string `json:"foreignAuthorId"`
	AuthorName      string `json:"authorName"`
	// Format, when sent, must be the format of the unit's files (ebook or
	// audiobook). Kept for clients that send it; it cannot turn audio files
	// into an ebook.
	Format string `json:"format"`
}

// Adopt handles POST /library/unmatched/{id}/adopt.
//
// Adoption is the scanner's own reconcile with a person supplying the match:
// the unit's files are registered in place with the same book_files write the
// scan uses (AddBookFileIfMissing is AddBookFile that also reports whether it
// inserted the row, which is what makes Undo exact). Nothing is moved, renamed
// or queued.
//
// A book added from metadata is created unmonitored and with its media type
// set to the adopted format, so a default of "both" cannot leave the other
// format Wanted and have it grabbed unasked. SkipCatalogueSync is set: the add
// never pulls the author's bibliography anyway (#1816), and skipping the
// single work fallback makes a provider failure fail at once instead of
// polling for 15 seconds, and means no background sync can create the book
// after this request has already given up on it.
//
// Atomicity is by compensation. The row is claimed first (S9); every file is
// checked on disk and against the library roots before anything is written
// (S10); each side effect is written into the row before the next one, so a
// request that dies part way can be reversed by RecoverStaleClaims; and if
// registering fails, what was registered is untracked and a book or author
// this request created is removed again.
func (h *AdoptionHandler) Adopt(w http.ResponseWriter, r *http.Request) {
	id, ok := unitIDParam(w, r)
	if !ok {
		return
	}
	var req adoptRequest
	if err := decodeAdoptionBody(w, r, 16<<10, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.ForeignBookID = strings.TrimSpace(req.ForeignBookID)
	if (req.BookID > 0) == (req.ForeignBookID != "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send either bookId or foreignBookId"})
		return
	}
	switch req.Format {
	case "", models.MediaTypeEbook, models.MediaTypeAudiobook:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format must be 'ebook' or 'audiobook'"})
		return
	}

	ctx := r.Context()
	token, err := h.units.Claim(ctx, id, db.UnmatchedStatePending, db.UnmatchedStateAdopting)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if token == "" {
		h.stateConflict(w, r, id)
		return
	}
	// The request context may be cancelled once work has started; releasing
	// the claim and compensating must still happen.
	work := context.WithoutCancel(ctx)
	message, err := h.adopt(work, id, token, req)
	if err != nil {
		var unfinished *unfinishedReversalError
		if errors.As(err, &unfinished) {
			// The claim and its record stay, so stale claim recovery can finish
			// the reversal; clearing them now would forget what to reverse.
			slog.Warn("adoption: adopt failed and could not be fully reversed", "unit", id,
				"error", unfinished.cause, "reverse_error", unfinished.reverse)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": unfinishedAdoptMessage})
			return
		}
		if _, rerr := h.units.ResetToPending(work, id, db.UnmatchedStateAdopting, token); rerr != nil {
			slog.Warn("adoption: could not release claim", "unit", id, "error", rerr)
		}
		h.writeAdoptionError(w, r, err)
		return
	}
	h.writeUnitWithMessage(w, r, id, message)
}

// alreadyHasFileMessage explains adopting into a book that already has a file
// of the same format on disk. Suggestions offer such books (#2879), because an
// untracked copy of a book already imported is common. The adopted files are
// added alongside: book_files is additive, the file the book already shows
// keeps showing, and nothing is deleted. Undo removes only what was added.
const alreadyHasFileMessage = "This book already had a file of this format. The adopted files were added alongside it, and the book still shows its existing file."

// adopt does the work of Adopt. The message it returns, when not empty,
// explains an outcome that is not the obvious one.
func (h *AdoptionHandler) adopt(ctx context.Context, id int64, token string, req adoptRequest) (string, error) {
	unit, err := h.units.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if unit == nil {
		return "", refuse(http.StatusNotFound, "unmatched book not found")
	}
	if req.Format != "" && req.Format != unit.Format {
		return "", refuse(http.StatusBadRequest, "These files are "+unit.Format+" files, so they can only be adopted as "+unit.Format+".")
	}
	format := unit.Format
	paths, err := h.registrationPaths(ctx, unit)
	if err != nil {
		return "", err
	}
	// Refuse before any side effect when a file already belongs to a book.
	if err := h.checkOwnership(ctx, append(paths, unit.MemberPaths...), 0); err != nil {
		return "", err
	}

	var book *models.Book
	var created addBookResult
	if req.BookID > 0 {
		if book, err = h.books.GetByID(ctx, req.BookID); err != nil {
			return "", err
		}
		if book == nil {
			return "", refuse(http.StatusNotFound, "That book is no longer in your library.")
		}
	} else {
		if h.adder == nil {
			return "", refuse(http.StatusServiceUnavailable, "Adding books is not available.")
		}
		unmonitored := false
		created, err = h.adder.addBookCore(ctx, addBookParams{
			ForeignBookID:     req.ForeignBookID,
			ForeignAuthorID:   strings.TrimSpace(req.ForeignAuthorID),
			AuthorName:        strings.TrimSpace(req.AuthorName),
			SearchOnAdd:       false,
			MediaType:         format,
			Monitored:         &unmonitored,
			SkipCatalogueSync: true,
		})
		var inLibrary *bookInLibraryError
		switch {
		case errors.As(err, &inLibrary):
			// Already in the library: adopt into that row, as if picked.
			book = inLibrary.Book
		case err != nil:
			return "", err
		default:
			book = created.Book
		}
	}

	message := ""
	if !created.BookCreated {
		had, err := h.hasLiveFile(ctx, book.ID, format)
		if err != nil {
			return "", err
		}
		if had {
			message = alreadyHasFileMessage
		}
	}

	rec := db.AdoptionRecord{BookID: book.ID}
	if created.BookCreated {
		rec.CreatedBookID = book.ID
	}
	if created.AuthorCreated && created.Author != nil {
		rec.CreatedAuthorID = created.Author.ID
	}
	// The created rows are on record before any file is registered.
	if err := h.progress(ctx, id, token, rec); err != nil {
		return "", h.failAdopt(ctx, id, rec, err)
	}

	regErr := h.register(ctx, id, token, book.ID, format, paths, unit.MemberPaths, &rec)
	if regErr == nil && rec.CreatedBookID > 0 {
		// A book this request created may also have picked up a file from
		// the add's own library lookup. Everything on it came from this
		// request, so Undo takes all of it. Then record the book as the
		// adoption leaves it, so Undo can tell whether anyone used it since.
		files, err := h.books.ListFiles(ctx, book.ID)
		if err != nil {
			regErr = err
		} else {
			rec.Registered = rec.Registered[:0]
			for _, f := range files {
				rec.Registered = append(rec.Registered, db.RegisteredFile{Path: f.Path, BookID: book.ID})
			}
			if rec.CreatedBookFingerprint, err = h.units.BookFingerprint(ctx, book.ID); err != nil {
				regErr = err
			}
		}
	}
	if regErr != nil {
		return "", h.failAdopt(ctx, id, rec, regErr)
	}
	done, err := h.units.CompleteAdoption(ctx, id, token, rec)
	if err != nil || !done {
		if err == nil {
			err = refuse(http.StatusConflict, "This book changed while it was being adopted. Try again.")
		}
		return "", h.failAdopt(ctx, id, rec, err)
	}
	slog.Info("adoption: registered files in place", "unit", id, "book_id", book.ID,
		"files", len(rec.Registered), "book_created", rec.CreatedBookID > 0, "author_created", rec.CreatedAuthorID > 0,
		"added_alongside_existing", message != "")
	return message, nil
}

// hasLiveFile reports whether the book has a registered file of format that
// still exists on disk.
func (h *AdoptionHandler) hasLiveFile(ctx context.Context, bookID int64, format string) (bool, error) {
	files, err := h.books.ListFiles(ctx, bookID)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		if f.Format == format && db.BookFilePathResolves(f.Path) {
			return true, nil
		}
	}
	return false, nil
}

// failAdopt reverses a failed adopt's writes. If the reversal itself fails
// the result says so, and the caller keeps the claim for recovery.
func (h *AdoptionHandler) failAdopt(ctx context.Context, id int64, rec db.AdoptionRecord, cause error) error {
	if _, err := h.reverse(ctx, id, rec); err != nil {
		return &unfinishedReversalError{cause: cause, reverse: err}
	}
	return cause
}

// progress writes what the adopt has done so far into its row, scoped to this
// request's claim.
func (h *AdoptionHandler) progress(ctx context.Context, id int64, token string, rec db.AdoptionRecord) error {
	ok, err := h.units.RecordAdoptionProgress(ctx, id, token, rec)
	if err != nil {
		return err
	}
	if !ok {
		return refuse(http.StatusConflict, "This book changed while it was being adopted. Try again.")
	}
	return nil
}

// registrationPaths decides what goes into book_files for a unit and checks
// every file first. An audiobook folder is registered folder by folder: the
// folder itself for loose tracks, and each disc folder of a disc set. That is
// the shape the scan already treats as tracked (a tracked audio file's folder
// covers its sibling tracks), so no disc rule is needed in the scanner.
// Anything else registers each member file.
//
// Each member must still be a regular file (Lstat, so a symlink swapped in
// since the scan is refused) and must resolve inside a library root: the row
// may be hours old (S10). The path registered is the one the scan walked, so
// the next scan recognises it.
func (h *AdoptionHandler) registrationPaths(ctx context.Context, unit *db.UnmatchedUnit) ([]string, error) {
	if len(unit.MemberPaths) == 0 {
		return nil, refuse(http.StatusUnprocessableEntity, "This book has no files left. Scan again to refresh the list.")
	}
	for _, p := range unit.MemberPaths {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, refuse(http.StatusUnprocessableEntity, "A file of this book is gone: "+filepath.Base(p)+". Scan again to refresh the list.")
		}
		if !info.Mode().IsRegular() {
			return nil, refuse(http.StatusUnprocessableEntity, filepath.Base(p)+" is not a regular file, so it cannot be adopted.")
		}
		if _, ok := h.roots.ResolveContained(ctx, p); !ok {
			return nil, refuse(http.StatusUnprocessableEntity, filepath.Base(p)+" is outside your library folders, so it cannot be adopted.")
		}
		// The list labels these and suggests nothing for them, but a row
		// stored by an older scan, or a request made by hand, would still
		// make a notes file a book's ebook (#2944).
		if importer.TooSmallToBeABook(p, info.Size()) {
			return nil, refuse(http.StatusBadRequest, fmt.Sprintf("%s is only %d bytes, too small to be a book, so it cannot be adopted.", filepath.Base(p), info.Size()))
		}
	}
	if unit.UnitKind != db.UnmatchedKindFolder || unit.Format != models.MediaTypeAudiobook {
		out := make([]string, len(unit.MemberPaths))
		for i, p := range unit.MemberPaths {
			out[i] = filepath.Clean(p)
		}
		return out, nil
	}
	var folders []string
	seen := make(map[string]bool)
	for _, p := range unit.MemberPaths {
		dir := filepath.Clean(filepath.Dir(p))
		if seen[dir] {
			continue
		}
		if _, ok := h.roots.ResolveContained(ctx, dir); !ok {
			return nil, refuse(http.StatusUnprocessableEntity, "This folder is outside your library folders, so it cannot be adopted.")
		}
		seen[dir] = true
		folders = append(folders, dir)
	}
	return folders, nil
}

// checkOwnership refuses when any path already belongs to a book other than
// bookID (0 means any book at all). A row left by a deleted book is not an
// owner: registering the file takes that row over, as an import does (#2937).
func (h *AdoptionHandler) checkOwnership(ctx context.Context, paths []string, bookID int64) error {
	for _, p := range paths {
		owned, err := h.books.PathOwnedByLiveOtherBook(ctx, p, bookID)
		if err != nil {
			return err
		}
		if owned {
			return refuse(http.StatusConflict, filepath.Base(p)+" already belongs to a book in your library.")
		}
	}
	return nil
}

// register writes the book_files rows, recording each one in rec and in the
// unit's row as soon as it is inserted. On error the caller reverses rec.
func (h *AdoptionHandler) register(ctx context.Context, unitID int64, token string, bookID int64, format string, paths, members []string, rec *db.AdoptionRecord) error {
	// Re-check against the resolved book: a concurrent import may have taken
	// a file in the moments since the first check.
	if err := h.checkOwnership(ctx, append(append([]string{}, paths...), members...), bookID); err != nil {
		return err
	}
	for _, p := range paths {
		inserted, err := h.registerFile(ctx, bookID, format, p)
		var owned *db.PathOwnedError
		if errors.As(err, &owned) {
			// Taken by another book between the check above and this write
			// (#2937): the same refusal the check gives, and the caller
			// reverses whatever this request already registered.
			return refuse(http.StatusConflict, filepath.Base(p)+" already belongs to a book in your library.")
		}
		if err != nil {
			return err
		}
		if inserted {
			rec.Registered = append(rec.Registered, db.RegisteredFile{Path: p, BookID: bookID})
			if err := h.progress(ctx, unitID, token, *rec); err != nil {
				return err
			}
		}
	}
	return nil
}
