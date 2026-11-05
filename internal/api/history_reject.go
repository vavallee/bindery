package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// WithLibraryRoots gives the handler the library roots Reject import checks
// before it deletes a file. Without them the action only detaches: a handler
// that cannot say where the library is must not unlink anything.
func (h *HistoryHandler) WithLibraryRoots(roots *LibraryRoots) *HistoryHandler {
	h.roots = roots
	return h
}

// rejectImportResponse tells the caller exactly what a reject did. It reports
// outcomes, not intentions: a file that could not be deleted is listed in
// KeptOnDisk, and an import that recorded no release says so in ReleaseKnown
// rather than the request failing.
type rejectImportResponse struct {
	// Status is the book's status after the reject ("wanted" when no file is
	// left for a monitored format).
	Status string `json:"status"`
	// Detached are the tracked file paths Bindery stopped tracking.
	Detached []string `json:"detached"`
	// Deleted are the detached paths that were also removed from disk.
	Deleted []string `json:"deleted"`
	// KeptOnDisk are detached paths left on disk: outside every library root,
	// or owned by another book.
	KeptOnDisk []string `json:"keptOnDisk"`
	// AlreadyGone are detached paths that were no longer on disk.
	AlreadyGone []string `json:"alreadyGone"`
	// ReleaseKnown is false for an import with no recorded release (a manual
	// import, or one from before #3248). Nothing could be blocklisted then.
	ReleaseKnown bool `json:"releaseKnown"`
	// Blocklisted is true when this call wrote the blocklist entry;
	// AlreadyBlocklisted when one already blocked the release for the book.
	Blocklisted        bool `json:"blocklisted"`
	AlreadyBlocklisted bool `json:"alreadyBlocklisted"`
	// NothingTracked is true when none of the import's files were tracked
	// any more (removed earlier, or an import that predates the paths the
	// event records), so no file was detached.
	NothingTracked bool `json:"nothingTracked"`
}

// importEventData is what an import history row carries that Reject import
// reads. Every field is optional: older rows lack most of them.
type importEventData struct {
	GUID   string   `json:"guid"`
	Format string   `json:"format"`
	Path   string   `json:"path"`
	Paths  []string `json:"paths"`
}

// importedPaths is the set of tracked paths the import placed. "paths" is the
// exact set (ebook imports since #2764); older rows only have "path", which
// is the file for a single file import and a shared folder otherwise. A folder
// is only ever acted on when it is itself the tracked row (an audiobook), so
// a shared parent directory can never become a deletion target.
func (d importEventData) importedPaths() []string {
	if len(d.Paths) > 0 {
		return d.Paths
	}
	if d.Path != "" {
		return []string{d.Path}
	}
	return nil
}

// RejectImport is the single "this import was wrong" action (#3250), for a
// bookImported History event: it detaches the files that import placed,
// blocklists the release it came from and returns the book to Wanted, in one
// database transaction.
//
// The file is deleted from disk only when it sits inside a library root and no
// other book owns it (safeRemoveBookPathExact); anywhere else it is detached
// and left alone, and the response says so. The disk step runs after the
// transaction commits, so a failure there never undoes the blocklist.
//
// The blocklist entry is book scoped, exactly as blocklisting an import from
// History is (releaseWideHistoryEvent): the release was wrong for this book's
// copy, which is no verdict on what another user's book may grab.
func (h *HistoryHandler) RejectImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	event, err := h.history.GetByID(ctx, id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if event == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "history event not found"})
		return
	}
	owner, _, err := h.history.GetOwnerByID(ctx, id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !canActOnHistoryEvent(ctx, owner) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "history event not found"})
		return
	}
	if event.EventType != models.HistoryEventBookImported {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only an import can be rejected"})
		return
	}
	if event.BookID == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the book this import belonged to no longer exists"})
		return
	}
	book, err := h.books.GetByID(ctx, *event.BookID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// A shared (unowned) book is not a non admin's to change, and a book that
	// is another user's does not exist for this caller.
	if book == nil || !auth.CheckWriteOwnership(ctx, book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "history event not found"})
		return
	}

	var data importEventData
	if event.Data != "" {
		if err := json.Unmarshal([]byte(event.Data), &data); err != nil {
			slog.Warn("reject import: corrupt history event data", "id", event.ID, "error", err)
		}
	}
	format := data.Format
	if format != models.MediaTypeEbook && format != models.MediaTypeAudiobook {
		format = ""
	}

	res, err := h.books.RejectImport(ctx, db.RejectImportInput{
		BookID:      book.ID,
		Paths:       data.importedPaths(),
		GUID:        data.GUID,
		Title:       event.SourceTitle,
		Scope:       models.BlocklistScopeBook,
		Reason:      models.HistoryEventBookImported,
		UserID:      auth.UserIDFromContext(ctx),
		OwnerUserID: auth.UserIDFromContext(ctx),
		HistoryData: func(res *db.RejectImportResult) string {
			out := map[string]any{"eventId": event.ID, "detached": res.DetachedPaths, "blocklisted": res.Blocklisted || res.AlreadyBlocklisted}
			if data.GUID != "" {
				out["guid"] = data.GUID
			}
			b, _ := json.Marshal(out)
			return string(b)
		},
	})
	if err != nil {
		writeServerError(w, r, err)
		return
	}

	resp := rejectImportResponse{
		Status:             res.Status,
		Detached:           orEmpty(res.DetachedPaths),
		Deleted:            []string{},
		KeptOnDisk:         []string{},
		AlreadyGone:        []string{},
		ReleaseKnown:       data.GUID != "",
		Blocklisted:        res.Blocklisted,
		AlreadyBlocklisted: res.AlreadyBlocklisted,
		NothingTracked:     len(res.DetachedPaths) == 0,
	}
	for _, p := range res.DetachedPaths {
		if _, statErr := os.Lstat(p); os.IsNotExist(statErr) {
			resp.AlreadyGone = append(resp.AlreadyGone, p)
			continue
		}
		if h.roots == nil {
			resp.KeptOnDisk = append(resp.KeptOnDisk, p)
			continue
		}
		skipped, rmErr := safeRemoveBookPathExact(ctx, h.roots, h.books, book.ID, p, format, "id", book.ID)
		if rmErr != nil {
			slog.Warn("reject import: could not delete file; it stays on disk", "book_id", book.ID, "path", filepath.Base(p), "error", rmErr)
			resp.KeptOnDisk = append(resp.KeptOnDisk, p)
			continue
		}
		if skipped {
			resp.KeptOnDisk = append(resp.KeptOnDisk, p)
			continue
		}
		resp.Deleted = append(resp.Deleted, p)
	}
	writeJSON(w, http.StatusOK, resp)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
