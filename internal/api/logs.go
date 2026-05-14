package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/logbuf"
)

// LogHandler exposes the log store over HTTP. When a LogRepo is attached it
// queries the persistent database; otherwise it falls back to the ring buffer.
type LogHandler struct {
	ring  *logbuf.Ring
	logs  *db.LogRepo    // optional persistent store
	dblog *db.LogHandler // optional; kept in sync with ring level
}

func NewLogHandler(ring *logbuf.Ring) *LogHandler {
	return &LogHandler{ring: ring}
}

// WithLogRepo attaches a persistent log repository so the handler queries the
// database when date/component/search filters are supplied.
func (h *LogHandler) WithLogRepo(logs *db.LogRepo) *LogHandler {
	h.logs = logs
	return h
}

// WithDBLogHandler stores a reference to the DB slog handler so that
// SetLevel propagates to it alongside the ring buffer.
func (h *LogHandler) WithDBLogHandler(dblog *db.LogHandler) *LogHandler {
	h.dblog = dblog
	return h
}

// List handles GET /api/v1/system/logs
//
// Query params:
//
//	level     — minimum level: debug | info | warn | error (default: info)
//	component — filter by component name
//	from      — RFC3339 start timestamp (inclusive)
//	to        — RFC3339 end timestamp (inclusive)
//	q         — full-text search in message + fields
//	limit     — max entries, 1–1000 (default: 200)
//	offset    — pagination offset (default: 0)
func (h *LogHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Level filter
	minLevel := slog.LevelInfo
	hasLevel := false
	if l := q.Get("level"); l != "" {
		minLevel = parseLevel(l)
		hasLevel = true
	}

	// Limit / offset
	limit := 200
	if ls := q.Get("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	offset := 0
	if os := q.Get("offset"); os != "" {
		if n, err := strconv.Atoi(os); err == nil && n >= 0 {
			offset = n
		}
	}

	// DB-backed query when a repo is attached and any DB-specific params are set.
	component := q.Get("component")
	fromStr := q.Get("from")
	toStr := q.Get("to")
	search := q.Get("q")

	useDB := h.logs != nil && (component != "" || fromStr != "" || toStr != "" || search != "" || offset > 0)

	if !useDB && h.logs != nil {
		// DB default: last hour when no date range given.
		useDB = true
	}

	if useDB {
		f := db.LogFilter{
			HasLevel:  hasLevel,
			Level:     minLevel,
			Component: component,
			Q:         search,
			Limit:     limit,
			Offset:    offset,
		}
		if fromStr != "" {
			if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
				f.FromTS = t
			}
		}
		if toStr != "" {
			if t, err := time.Parse(time.RFC3339, toStr); err == nil {
				f.ToTS = t
			}
		}
		// Default to the last hour when no explicit date range is supplied.
		if f.FromTS.IsZero() && f.ToTS.IsZero() {
			f.FromTS = time.Now().UTC().Add(-time.Hour)
		}

		entries, err := h.logs.Query(r.Context(), f)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if entries == nil {
			entries = []db.LogEntry{}
		}
		writeJSON(w, http.StatusOK, entries)
		return
	}

	// Ring buffer fallback (no DB attached).
	entries := h.ring.Snapshot(minLevel, limit)
	if entries == nil {
		entries = []logbuf.Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// SetLevel handles PUT /api/v1/system/loglevel
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
	if h.dblog != nil {
		h.dblog.SetLevel(level)
	}
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
