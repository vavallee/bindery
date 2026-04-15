package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/logbuf"
)

// LogHandler exposes the in-process ring buffer over HTTP.
type LogHandler struct {
	ring *logbuf.Ring
}

func NewLogHandler(ring *logbuf.Ring) *LogHandler {
	return &LogHandler{ring: ring}
}

// List handles GET /api/v1/system/logs
//
// Query params:
//
//	level  — minimum level to return: debug | info | warn | error (default: info)
//	limit  — max entries to return, 1–1000 (default: 200)
func (h *LogHandler) List(w http.ResponseWriter, r *http.Request) {
	minLevel := slog.LevelInfo
	if l := r.URL.Query().Get("level"); l != "" {
		minLevel = parseLevel(l)
	}

	limit := 200
	if ls := r.URL.Query().Get("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}

	entries := h.ring.Snapshot(minLevel, limit)
	if entries == nil {
		entries = []logbuf.Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// SetLevel handles PUT /api/v1/system/loglevel
//
// Body: {"level": "debug"} — changes the runtime log level.
// Affects both the ring buffer threshold and the global slog handler.
func (h *LogHandler) SetLevel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Level == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "level required"})
		return
	}
	level := parseLevel(req.Level)
	h.ring.SetLevel(level)
	writeJSON(w, http.StatusOK, map[string]string{"level": level.String()})
}

// GetLevel handles GET /api/v1/system/loglevel
func (h *LogHandler) GetLevel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"level": h.ring.Level().String()})
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
