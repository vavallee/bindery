package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type BlocklistHandler struct {
	blocklist *db.BlocklistRepo
}

func NewBlocklistHandler(blocklist *db.BlocklistRepo) *BlocklistHandler {
	return &BlocklistHandler{blocklist: blocklist}
}

func (h *BlocklistHandler) List(w http.ResponseWriter, r *http.Request) {
	entries, err := h.blocklist.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if entries == nil {
		entries = []models.BlocklistEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *BlocklistHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.blocklist.DeleteByID(r.Context(), id); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *BlocklistHandler) BulkDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// The body cap now applies to DELETE too (#2354). Report an
		// over-limit body as 413 rather than folding it into "invalid
		// request body", which reads as a client JSON bug.
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	for _, id := range req.IDs {
		if err := h.blocklist.DeleteByID(r.Context(), id); err != nil {
			writeServerError(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
