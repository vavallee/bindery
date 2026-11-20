package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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
	// ExternalImportFormats lists the formats whose effective import mode is
	// External (another tool owns their layout, #3946).
	ExternalImportFormats(ctx context.Context) []string
	// ReorganizeFileFormats lists the distinct formats of the given files.
	ReorganizeFileFormats(ctx context.Context, fileIDs []int64) []string
}

// codeReorganizeExternalMode is the stable error code of the 409 Apply
// returns for files of a format in External import mode without confirmation.
const codeReorganizeExternalMode = "reorganize_external_mode"

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
	// metadata.opf counts (#4008), only present when import.write_opf_sidecar
	// produced an action. ToWrite is the preview, the rest are apply results.
	SidecarToWrite   int `json:"sidecarToWrite,omitempty"`
	SidecarWritten   int `json:"sidecarWritten,omitempty"`
	SidecarUnchanged int `json:"sidecarUnchanged,omitempty"`
	SidecarForeign   int `json:"sidecarForeign,omitempty"`
	SidecarFailed    int `json:"sidecarFailed,omitempty"`
	SidecarShared    int `json:"sidecarShared,omitempty"`
}

type reorganizeResponse struct {
	Moves   []importer.ReorganizeMove `json:"moves"`
	Summary reorganizeSummary         `json:"summary"`
	// ExternalMode is true on a preview when the plan holds files of a format
	// whose import mode is External. ExternalFormats names them and Warning
	// explains why Apply needs confirmExternalMode (#3946).
	ExternalMode    bool     `json:"externalMode"`
	ExternalFormats []string `json:"externalFormats,omitempty"`
	Warning         string   `json:"warning,omitempty"`
}

const externalModeWarning = "Import mode is External for %s, so another tool owns that layout. Reorganize would rename and move those files to Bindery's naming template and can fight that tool. Apply needs explicit confirmation."

// newReorganizeResponse builds the response body, normalising a nil moves
// slice to an empty one so the JSON is "moves": [] rather than null (#3470).
func newReorganizeResponse(moves []importer.ReorganizeMove) reorganizeResponse {
	if moves == nil {
		moves = []importer.ReorganizeMove{}
	}
	return reorganizeResponse{Moves: moves, Summary: summarize(moves)}
}

func summarize(moves []importer.ReorganizeMove) reorganizeSummary {
	s := reorganizeSummary{Total: len(moves)}
	for _, m := range moves {
		switch m.Sidecar {
		case importer.ReorgSidecarWrite:
			s.SidecarToWrite++
		case importer.ReorgSidecarWritten:
			s.SidecarWritten++
		case importer.ReorgSidecarUnchanged:
			s.SidecarUnchanged++
		case importer.ReorgSidecarForeign:
			s.SidecarForeign++
		case importer.ReorgSidecarFailed:
			s.SidecarFailed++
		case importer.ReorgSidecarShared:
			s.SidecarShared++
		}
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
	resp := newReorganizeResponse(moves)
	// Only rows that would move count: those are the fileIds the dialog
	// submits to Apply, so a format whose files are all already in place, or
	// missing, or blocked by a collision, is not one Reorganize would touch.
	var present []string
	for _, m := range moves {
		if m.Status == importer.ReorgStatusMove {
			present = append(present, m.Format)
		}
	}
	if ext := intersectFormats(h.scanner.ExternalImportFormats(ctx), present); len(ext) > 0 {
		resp.ExternalMode = true
		resp.ExternalFormats = ext
		resp.Warning = fmt.Sprintf(externalModeWarning, strings.Join(ext, " and "))
	}
	writeJSON(w, http.StatusOK, resp)
}

// intersectFormats returns the members of external that appear in present,
// in external's order.
func intersectFormats(external, present []string) []string {
	var out []string
	for _, e := range external {
		for _, p := range present {
			if e == p {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

type applyReorganizeRequest struct {
	FileIDs []int64 `json:"fileIds"`
	// ConfirmExternalMode lets an admin deliberately reorganize files of a
	// format in External import mode (#3946).
	ConfirmExternalMode bool `json:"confirmExternalMode"`
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
	// A request that holds any file of an External-mode format is refused whole
	// unless confirmed, so nothing of that format is ever moved silently. Files
	// of managed formats can be applied in their own request.
	if !req.ConfirmExternalMode {
		ext := intersectFormats(h.scanner.ExternalImportFormats(r.Context()), h.scanner.ReorganizeFileFormats(r.Context(), req.FileIDs))
		if len(ext) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   fmt.Sprintf(externalModeWarning, strings.Join(ext, " and ")),
				"code":    codeReorganizeExternalMode,
				"formats": ext,
			})
			return
		}
	}
	results := h.scanner.ApplyReorganize(r.Context(), req.FileIDs)
	writeJSON(w, http.StatusOK, newReorganizeResponse(results))
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
