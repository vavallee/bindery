package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/vavallee/bindery/internal/importer"
)

// reorganizeScanner is the slice of the import scanner the reorganize endpoints
// use: compute where tracked files should live under the current naming
// template, and move them there (#1181).
type reorganizeScanner interface {
	PreviewReorganizeBook(ctx context.Context, bookID int64) ([]importer.ReorganizeMove, error)
	PreviewReorganizeAuthor(ctx context.Context, authorID int64) ([]importer.ReorganizeMove, error)
	PreviewReorganizeLibrary(ctx context.Context) ([]importer.ReorganizeMove, error)
	ApplyReorganize(ctx context.Context, fileIDs []int64) []importer.ReorganizeMove
}

// ReorganizeHandler serves the library-reorganize preview and apply endpoints.
// All routes are admin-only (they move files on the server filesystem) and
// mounted behind auth.RequireAdmin in main.go.
type ReorganizeHandler struct {
	scanner reorganizeScanner
}

// NewReorganizeHandler builds the handler over the import scanner.
func NewReorganizeHandler(scanner reorganizeScanner) *ReorganizeHandler {
	return &ReorganizeHandler{scanner: scanner}
}

type reorganizeSummary struct {
	Total     int `json:"total"`
	ToMove    int `json:"toMove"`
	Noop      int `json:"noop"`
	Collision int `json:"collision"`
	Missing   int `json:"missing"`
	Errored   int `json:"errored"`
	Moved     int `json:"moved"`
	Failed    int `json:"failed"`
}

type reorganizeResponse struct {
	Moves   []importer.ReorganizeMove `json:"moves"`
	Summary reorganizeSummary         `json:"summary"`
}

func summarize(moves []importer.ReorganizeMove) reorganizeSummary {
	s := reorganizeSummary{Total: len(moves)}
	for _, m := range moves {
		switch m.Status {
		case importer.ReorgStatusMove:
			s.ToMove++
		case importer.ReorgStatusNoop:
			s.Noop++
		case importer.ReorgStatusCollision:
			s.Collision++
		case importer.ReorgStatusMissing:
			s.Missing++
		case importer.ReorgStatusError:
			s.Errored++
		case importer.ReorgStatusMoved:
			s.Moved++
		case importer.ReorgStatusFailed:
			s.Failed++
		}
	}
	return s
}

// Preview handles GET /api/v1/reorganize/preview?scope=book|author|library&id=N
// It computes the proposed moves for the requested scope without touching disk.
func (h *ReorganizeHandler) Preview(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	ctx := r.Context()

	var (
		moves []importer.ReorganizeMove
		err   error
	)
	switch scope {
	case "book":
		id, ok := parseReorgID(w, r)
		if !ok {
			return
		}
		moves, err = h.scanner.PreviewReorganizeBook(ctx, id)
	case "author":
		id, ok := parseReorgID(w, r)
		if !ok {
			return
		}
		moves, err = h.scanner.PreviewReorganizeAuthor(ctx, id)
	case "library":
		moves, err = h.scanner.PreviewReorganizeLibrary(ctx)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scope must be one of: book, author, library"})
		return
	}
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reorganizeResponse{Moves: moves, Summary: summarize(moves)})
}

type applyReorganizeRequest struct {
	FileIDs []int64 `json:"fileIds"`
}

// Apply handles POST /api/v1/reorganize/apply with a body of {fileIds:[...]}.
// It recomputes each file's destination server-side (never trusting a
// client-supplied target) and moves the ones still classified as a clean move,
// returning a result per file.
func (h *ReorganizeHandler) Apply(w http.ResponseWriter, r *http.Request) {
	var req applyReorganizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.FileIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fileIds is required"})
		return
	}
	results := h.scanner.ApplyReorganize(r.Context(), req.FileIDs)
	writeJSON(w, http.StatusOK, reorganizeResponse{Moves: results, Summary: summarize(results)})
}

// parseReorgID reads and validates the required ?id= query parameter for the
// book/author scopes, writing a 400 and returning ok=false when it is absent or
// malformed.
func parseReorgID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a positive id is required for this scope"})
		return 0, false
	}
	return id, true
}
