package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type HistoryHandler struct {
	history   *db.HistoryRepo
	blocklist *db.BlocklistRepo
}

func NewHistoryHandler(history *db.HistoryRepo, blocklist *db.BlocklistRepo) *HistoryHandler {
	return &HistoryHandler{history: history, blocklist: blocklist}
}

// historyListResponse is the paginated wrapper returned by List. Replaces the
// pre-Wave-2 bare `[]models.HistoryEvent` shape; clients must unwrap `items`
// to reach the rows. See the Wave 2 / E PR for the breaking-change disclosure.
type historyListResponse struct {
	Items  []models.HistoryEvent `json:"items"`
	Total  int                   `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

const (
	historyListDefaultLimit = 100
	historyListMaxLimit     = 500
)

func (h *HistoryHandler) List(w http.ResponseWriter, r *http.Request) {
	bookIDStr := r.URL.Query().Get("bookId")
	eventType := r.URL.Query().Get("eventType")
	limit, offset := parseLimitOffset(r, historyListDefaultLimit, historyListMaxLimit)

	opts := db.HistoryListOpts{
		EventType: eventType,
		Limit:     limit,
		Offset:    offset,
	}
	if bookIDStr != "" {
		// Tolerate junk in the query string the same way the pre-Wave-2 code
		// did (strconv error gives id=0, which then matches no rows).
		opts.BookID, _ = strconv.ParseInt(bookIDStr, 10, 64)
	}
	// Per-user scoping (D3): when EnforceTenancy is on and the caller is a
	// non-admin user, restrict to events whose book is owned by them. The
	// JOIN-shape lives inside ListPage so paginated and filtered queries see
	// the same scoping rule.
	ctx := r.Context()
	if auth.EnforceTenancy() && auth.UserRoleFromContext(ctx) != "admin" {
		if uid := auth.UserIDFromContext(ctx); uid != 0 {
			opts.UserID = uid
		}
	}

	events, total, err := h.history.ListPage(ctx, opts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if events == nil {
		events = []models.HistoryEvent{}
	}
	writeJSON(w, http.StatusOK, historyListResponse{
		Items:  events,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

func (h *HistoryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	// Per-user scoping (D3): JOIN through books to find owner before delete.
	owner, exists, err := h.history.GetOwnerByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "history event not found"})
		return
	}
	if !auth.CheckOwnership(r.Context(), owner) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "history event not found"})
		return
	}
	if err := h.history.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Blocklist adds a history event's release to the blocklist so it won't be grabbed again.
func (h *HistoryHandler) Blocklist(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	event, err := h.history.GetByID(r.Context(), id)
	if err != nil || event == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "history event not found"})
		return
	}

	// Per-user scoping (D3): blocklisting reaches into the user's library —
	// must not let user B promote user A's grab/import event to a blocklist
	// entry, which would (1) leak that the event exists and (2) pollute A's
	// search results.
	owner, _, err := h.history.GetOwnerByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !auth.CheckOwnership(r.Context(), owner) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "history event not found"})
		return
	}

	// Extract guid from the stored event data
	var data map[string]interface{}
	if event.Data != "" {
		if err := json.Unmarshal([]byte(event.Data), &data); err != nil {
			slog.Warn("corrupt history event data", "id", event.ID, "error", err)
		}
	}
	guid, _ := data["guid"].(string)

	// Fall back to sourceTitle as a unique key if no guid stored
	if guid == "" {
		guid = event.SourceTitle
	}

	// Idempotent: blocklisting the same release twice must not create a
	// duplicate row. Create has no unique constraint to lean on.
	if guid != "" {
		blocked, err := h.blocklist.IsBlocked(r.Context(), guid)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if blocked {
			writeJSON(w, http.StatusOK, map[string]string{"status": "already blocklisted"})
			return
		}
	}

	entry := &models.BlocklistEntry{
		BookID: event.BookID,
		GUID:   guid,
		Title:  event.SourceTitle,
		Reason: event.EventType,
	}
	// D4b audit: this is a user-driven write (someone pressed "Blocklist" in
	// the history UI), so tag the row with their id. The list/IsBlocked
	// queries are global and don't read this column; it's surfaced through
	// the API for admin views only. UserIDFromContext returns 0 for
	// unauthenticated requests, in which case fall back to the system-write
	// path so we don't insert a fake user id.
	if uid := auth.UserIDFromContext(r.Context()); uid != 0 {
		err = h.blocklist.CreateByUser(r.Context(), entry, uid)
	} else {
		err = h.blocklist.Create(r.Context(), entry)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}
