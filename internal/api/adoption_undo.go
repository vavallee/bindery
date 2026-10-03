package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Undo, the compensation a failed adopt runs, and the recovery of claims a
// dead request left behind all reverse an adoption the same way: reverse.

// keptBookMessage is what Undo says when the created book stays.
const keptBookMessage = "The files are no longer adopted. The book it added stays in your library because it has been used since."

// Answers when a reversal could not finish. The claim and the record stay, so
// stale claim recovery completes the reversal later.
const (
	unfinishedUndoMessage  = "Undo could not finish, most likely because the database was busy. Nothing is lost: it will be completed automatically within a few minutes, or try again then."
	unfinishedAdoptMessage = "Adopting failed and could not be fully reversed yet. It will be cleaned up automatically within a few minutes."
)

// unfinishedReversalError is a failed adopt whose reversal also failed.
type unfinishedReversalError struct {
	cause, reverse error
}

func (e *unfinishedReversalError) Error() string {
	return fmt.Sprintf("%v (reversal unfinished: %v)", e.cause, e.reverse)
}

func (e *unfinishedReversalError) Unwrap() error { return e.cause }

// Undo handles POST /library/unmatched/{id}/undo. It untracks exactly the
// book_files rows the adoption registered, each only while it still belongs
// to the book it was registered to, then removes a book or author the
// adoption created when nothing else holds it and nobody has used it since
// (S9). The unit returns to pending only once all of that succeeded; if any
// step fails the row keeps its claim and record and the answer is 503, so
// nothing about the adoption is forgotten while files may still be tracked.
func (h *AdoptionHandler) Undo(w http.ResponseWriter, r *http.Request) {
	id, ok := unitIDParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	token, err := h.units.Claim(ctx, id, db.UnmatchedStateAdopted, db.UnmatchedStateUndoing)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if token == "" {
		h.stateConflict(w, r, id)
		return
	}
	work := context.WithoutCancel(ctx)
	unit, err := h.units.Get(work, id)
	if err != nil || unit == nil {
		if _, rerr := h.units.ReleaseClaim(work, id, db.UnmatchedStateUndoing, db.UnmatchedStateAdopted, token); rerr != nil {
			slog.Warn("adoption: could not release undo claim", "unit", id, "error", rerr)
		}
		if err == nil {
			err = refuse(http.StatusNotFound, "unmatched book not found")
		}
		h.writeAdoptionError(w, r, err)
		return
	}
	kept, err := h.reverse(work, id, recordOf(unit))
	if err != nil {
		slog.Warn("adoption: undo could not finish; left for recovery", "unit", id, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": unfinishedUndoMessage})
		return
	}
	done, err := h.units.CompleteUndo(work, id, token)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !done {
		h.stateConflict(w, r, id)
		return
	}
	slog.Info("adoption: undone", "unit", id, "book_id", unit.BookID, "files", len(unit.Registered), "kept_created_book", kept)
	message := ""
	if kept {
		message = keptBookMessage
	}
	h.writeUnitWithMessage(w, r, id, message)
}

func recordOf(u *db.UnmatchedUnit) db.AdoptionRecord {
	return db.AdoptionRecord{
		BookID: u.BookID, PriorBookStatus: u.PriorBookStatus, CreatedBookID: u.CreatedBookID, CreatedAuthorID: u.CreatedAuthorID,
		CreatedBookFingerprint: u.CreatedBookFingerprint, Registered: u.Registered,
	}
}

// reverse undoes an adoption's writes and reports whether a created book was
// kept because it has been used since. Every step is idempotent, so a reversal
// that fails part way can simply run again; the first error stops it and is
// returned, and the caller must then keep the record.
//
// The fingerprint is compared before untracking, because untracking itself
// updates the book.
func (h *AdoptionHandler) reverse(ctx context.Context, unitID int64, rec db.AdoptionRecord) (bool, error) {
	used := false
	if rec.CreatedBookID > 0 && rec.CreatedBookFingerprint != "" {
		now, err := h.units.BookFingerprint(ctx, rec.CreatedBookID)
		if err != nil {
			return false, fmt.Errorf("fingerprint created book %d: %w", rec.CreatedBookID, err)
		}
		used = now != "" && now != rec.CreatedBookFingerprint
	}
	for _, f := range rec.Registered {
		if _, err := h.untrackFile(ctx, f.Path, f.BookID); err != nil {
			return false, fmt.Errorf("untrack %s: %w", f.Path, err)
		}
	}
	if rec.CreatedBookID == 0 && rec.BookID > 0 && rec.PriorBookStatus == models.BookStatusSkipped {
		b, err := h.books.GetByID(ctx, rec.BookID)
		if err != nil {
			return false, fmt.Errorf("load book %d to restore prior status: %w", rec.BookID, err)
		}
		if b != nil && b.Status == models.BookStatusWanted {
			b.Status = models.BookStatusSkipped
			b.Monitored = false
			if err := h.books.Update(ctx, b); err != nil {
				return false, fmt.Errorf("restore skipped status on book %d: %w", rec.BookID, err)
			}
		}
	}
	if used {
		return true, nil
	}
	if rec.CreatedBookID > 0 {
		ours, err := h.bookIsOnlyOurs(ctx, unitID, rec.CreatedBookID)
		if err != nil {
			return false, err
		}
		if ours {
			if err := h.books.Delete(ctx, rec.CreatedBookID); err != nil {
				return false, fmt.Errorf("remove created book %d: %w", rec.CreatedBookID, err)
			}
		}
	}
	if rec.CreatedAuthorID > 0 {
		// Including excluded books: deleting the author cascades to them.
		books, err := h.books.ListByAuthorIncludingExcluded(ctx, rec.CreatedAuthorID)
		if err != nil {
			return false, fmt.Errorf("list books of created author %d: %w", rec.CreatedAuthorID, err)
		}
		if len(books) > 0 {
			return false, nil
		}
		other, err := h.units.AuthorReferencedElsewhere(ctx, rec.CreatedAuthorID, unitID)
		if err != nil {
			return false, err
		}
		if other {
			return false, nil
		}
		if err := h.authors.Delete(ctx, rec.CreatedAuthorID); err != nil {
			return false, fmt.Errorf("remove created author %d: %w", rec.CreatedAuthorID, err)
		}
	}
	return false, nil
}

func (h *AdoptionHandler) bookIsOnlyOurs(ctx context.Context, unitID, bookID int64) (bool, error) {
	files, err := h.books.ListFiles(ctx, bookID)
	if err != nil {
		return false, fmt.Errorf("list files of created book %d: %w", bookID, err)
	}
	if len(files) > 0 {
		return false, nil
	}
	other, err := h.units.BookReferencedElsewhere(ctx, bookID, unitID)
	if err != nil {
		return false, err
	}
	return !other, nil
}

// RecoverStaleClaims reverses and releases rows an adopt or undo claimed
// longer than olderThan ago: requests that died holding them. The row records
// every side effect before the next one, so the reversal is the same one Undo
// runs, with the same limits. A reversal that fails leaves the row claimed for
// the next pass. Every write is scoped to the stale claim's token, so a row
// another request has claimed since is left alone. Call it with zero at
// startup, when any claim is from a previous process, and with
// db.UnmatchedClaimTimeout later.
func (h *AdoptionHandler) RecoverStaleClaims(ctx context.Context, olderThan time.Duration) (int, error) {
	stale, err := h.units.StaleClaims(ctx, time.Now().Add(-olderThan))
	if err != nil {
		return 0, err
	}
	recovered := 0
	var errs []error
	for i := range stale {
		u := &stale[i]
		if _, err := h.reverse(ctx, u.ID, recordOf(u)); err != nil {
			slog.Warn("adoption: could not reverse an abandoned claim; will retry", "unit", u.ID, "error", err)
			errs = append(errs, err)
			continue
		}
		ok, err := h.units.ResetToPending(ctx, u.ID, u.State, u.ClaimToken)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			recovered++
			slog.Warn("adoption: recovered an abandoned claim", "unit", u.ID, "state", u.State, "files", len(u.Registered))
		}
	}
	return recovered, errors.Join(errs...)
}
