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

func (h *HistoryHandler) List(w http.ResponseWriter, r *http.Request) {
	var events []models.HistoryEvent
	var err error

	bookIDStr := r.URL.Query().Get("bookId")
	eventType := r.URL.Query().Get("eventType")

	switch {
	case bookIDStr != "":
		id, _ := strconv.ParseInt(bookIDStr, 10, 64)
		events, err = h.history.ListByBook(r.Context(), id)
	case eventType != "":
		events, err = h.history.ListByType(r.Context(), eventType)
	default:
		events, err = h.history.List(r.Context())
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if events == nil {
		events = []models.HistoryEvent{}
	}
	writeJSON(w, http.StatusOK, events)
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
