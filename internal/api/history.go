package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
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

	events, total, err := h.history.ListPage(r.Context(), opts)
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
	if err := h.blocklist.Create(r.Context(), entry); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}
