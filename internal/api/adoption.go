package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
)

// Library adoption (/library/unmatched): the books a library scan could not
// match, and the decisions an admin makes about them. Every route is admin
// only (registerAdoptionRoutes). No request carries a path: rows are addressed
// by id and every path is resolved on the server (S9, S10).

// maxAdoptionMembersShown bounds the file names a row carries for display.
const maxAdoptionMembersShown = 20

// adoptionScanner is the part of the scanner the adoption list reads.
type adoptionScanner interface {
	ScanRunning() bool
	RootFormat(root string) string
}

// adoptionBookAdder is the add book core (see add_book_core.go).
type adoptionBookAdder interface {
	addBookCore(ctx context.Context, req addBookParams) (addBookResult, error)
}

// adoptionUnitStore is the unmatched unit repository surface the handler uses
// (db.UnmatchedUnitRepo). An interface so a test can count the queries a page
// costs.
type adoptionUnitStore interface {
	List(ctx context.Context, q db.UnmatchedListQuery) ([]db.UnmatchedUnit, int, error)
	Facets(ctx context.Context, q db.UnmatchedListQuery) (db.UnmatchedFacets, error)
	Summary(ctx context.Context) (db.UnmatchedSummary, error)
	Get(ctx context.Context, id int64) (*db.UnmatchedUnit, error)
	BookRefs(ctx context.Context, ids []int64) (map[int64]db.UnmatchedBookRef, error)
	ClaimState(ctx context.Context, id int64, from, to string) (bool, error)
	Claim(ctx context.Context, id int64, from, to string) (string, error)
	ReleaseClaim(ctx context.Context, id int64, from, to, token string) (bool, error)
	RecordAdoptionProgress(ctx context.Context, id int64, token string, rec db.AdoptionRecord) (bool, error)
	CompleteAdoption(ctx context.Context, id int64, token string, rec db.AdoptionRecord) (bool, error)
	CompleteUndo(ctx context.Context, id int64, token string) (bool, error)
	ResetToPending(ctx context.Context, id int64, from, token string) (bool, error)
	StaleClaims(ctx context.Context, before time.Time) ([]db.UnmatchedUnit, error)
	BookFingerprint(ctx context.Context, bookID int64) (string, error)
	IgnorePending(ctx context.Context, ids []int64, authorFolder string) (int64, error)
	BookReferencedElsewhere(ctx context.Context, bookID, exceptID int64) (bool, error)
	AuthorReferencedElsewhere(ctx context.Context, authorID, exceptID int64) (bool, error)
}

// AdoptionHandler serves the adoption view on the Import page.
type AdoptionHandler struct {
	units    adoptionUnitStore
	books    *db.BookRepo
	authors  *db.AuthorRepo
	adder    adoptionBookAdder
	roots    *LibraryRoots
	scanner  adoptionScanner
	settings *db.SettingsRepo
	// registerFile records one file against a book and reports whether this
	// call inserted it. A seam so tests can fail registration part way.
	registerFile func(ctx context.Context, bookID int64, format, path string) (bool, error)
	// untrackFile removes one registered row while it still belongs to bookID.
	// A seam so tests can fail a reversal part way.
	untrackFile func(ctx context.Context, path string, bookID int64) (bool, error)
}

// NewAdoptionHandler wires the adoption routes. roots must be the library
// roots (not the download directories): adoption only ever registers files
// that already live in the library.
func NewAdoptionHandler(units *db.UnmatchedUnitRepo, books *db.BookRepo, authors *db.AuthorRepo,
	authorHandler *AuthorHandler, roots *LibraryRoots, scanner *importer.Scanner, settings *db.SettingsRepo) *AdoptionHandler {
	h := &AdoptionHandler{units: units, books: books, authors: authors, roots: roots, settings: settings}
	if authorHandler != nil {
		h.adder = authorHandler
	}
	if scanner != nil {
		h.scanner = scanner
	}
	h.registerFile = books.AddBookFileIfMissing
	h.untrackFile = books.UntrackFilePathForBook
	return h
}

// adoptionCandidate is one suggestion with its book.
type adoptionCandidate struct {
	Book  db.UnmatchedBookRef `json:"book"`
	Score float64             `json:"score"`
	// FolderAuthorOnly is set on a row with an author conflict for a book
	// that is not by the author the files name: a look alike from the
	// folder's author, which the page never preselects (#2942).
	FolderAuthorOnly bool `json:"folderAuthorOnly,omitempty"`
}

// adoptionAuthorConflict says which authors disagree when a row's files name
// someone other than its author folder (#2942).
type adoptionAuthorConflict struct {
	Files  string `json:"files"`
	Folder string `json:"folder"`
}

// adoptionItem is one row as the web sees it.
type adoptionItem struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	Format       string `json:"format"`
	FileCount    int    `json:"fileCount"`
	SizeBytes    int64  `json:"sizeBytes"`
	RelPath      string `json:"relPath"`
	RootPath     string `json:"rootPath"`
	AuthorFolder string `json:"authorFolder"`
	ParsedTitle  string `json:"parsedTitle"`
	ParsedAuthor string `json:"parsedAuthor"`
	Reason       string `json:"reason"`
	// AuthorConflict is set when the parsed author (what the files' tags or
	// names say) is not the author folder's.
	AuthorConflict *adoptionAuthorConflict `json:"authorConflict,omitempty"`
	Candidates     []adoptionCandidate     `json:"candidates"`
	TopScore       float64                 `json:"topScore"`
	State          string                  `json:"state"`
	Book           *db.UnmatchedBookRef    `json:"book,omitempty"`
	// RootFormat is the format the row's library root holds, "ebook" or
	// "audiobook", when the ebook and audiobook roots are separate folders,
	// and "" with one combined root. The web labels a row whose own format
	// differs, such as an ebook found under the audiobooks root (#2944).
	RootFormat string `json:"rootFormat,omitempty"`
	// BookCreated and AuthorCreated say whether the adoption added them, which
	// is also what Undo would remove.
	BookCreated   bool       `json:"bookCreated"`
	AuthorCreated bool       `json:"authorCreated"`
	Members       []string   `json:"members"`
	FirstSeenAt   time.Time  `json:"firstSeenAt"`
	ResolvedAt    *time.Time `json:"resolvedAt,omitempty"`
	// Message explains an outcome that is not the obvious one, such as Undo
	// keeping a book that has been used since it was added.
	Message string `json:"message,omitempty"`
}

// adoptionScanStatus is what the page says about the last and current scan.
type adoptionScanStatus struct {
	Ran          bool   `json:"ran"`
	RanAt        string `json:"ranAt,omitempty"`
	Running      bool   `json:"running"`
	FilesFound   int    `json:"filesFound"`
	Truncated    bool   `json:"truncated"`
	Error        string `json:"error,omitempty"`
	NoFilesFound bool   `json:"noFilesFound"`
}

type adoptionListResponse struct {
	Items   []adoptionItem      `json:"items"`
	Total   int                 `json:"total"`
	Facets  *db.UnmatchedFacets `json:"facets,omitempty"`
	Summary db.UnmatchedSummary `json:"summary"`
	Scan    adoptionScanStatus  `json:"scan"`
}

type adoptionSummaryResponse struct {
	db.UnmatchedSummary
	Scan adoptionScanStatus `json:"scan"`
}

// List handles GET /library/unmatched. Facets are computed only when asked
// for (facets=1), which the web does when the state or search changes, not
// on every page turn (P4). Candidates and adopted books for the whole page
// are hydrated in one query.
func (h *AdoptionHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Release rows a request died holding, reversing what it did, so they do
	// not sit invisible. Cheap: an indexed state lookup that is usually empty.
	if _, err := h.RecoverStaleClaims(context.WithoutCancel(ctx), db.UnmatchedClaimTimeout); err != nil {
		slog.Warn("adoption: stale claim recovery failed", "error", err)
	}
	qv := r.URL.Query()
	limit, _ := strconv.Atoi(qv.Get("limit"))
	offset, _ := strconv.Atoi(qv.Get("offset"))
	q := db.UnmatchedListQuery{
		State:        qv.Get("state"),
		Reason:       qv.Get("reason"),
		AuthorFolder: qv.Get("authorFolder"),
		Format:       qv.Get("format"),
		Search:       qv.Get("search"),
		Sort:         qv.Get("sort"),
		Dir:          qv.Get("dir"),
		Limit:        limit,
		Offset:       offset,
	}
	units, total, err := h.units.List(ctx, q)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	items, err := h.hydrate(ctx, units)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	resp := adoptionListResponse{Items: items, Total: total, Scan: h.scanStatus(ctx)}
	if qv.Get("facets") == "1" {
		f, err := h.units.Facets(ctx, q)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
		resp.Facets = &f
	}
	if resp.Summary, err = h.units.Summary(ctx); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Summary handles GET /library/unmatched/summary: the counts behind the nav
// badge and the summary strip, and the scan status the page polls while a
// scan runs (P5).
func (h *AdoptionHandler) Summary(w http.ResponseWriter, r *http.Request) {
	sum, err := h.units.Summary(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adoptionSummaryResponse{UnmatchedSummary: sum, Scan: h.scanStatus(r.Context())})
}

// hydrate turns rows into items, loading every book they point at at once.
func (h *AdoptionHandler) hydrate(ctx context.Context, units []db.UnmatchedUnit) ([]adoptionItem, error) {
	seen := make(map[int64]bool)
	var ids []int64
	want := func(id int64) {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for i := range units {
		want(units[i].BookID)
		for _, c := range units[i].Candidates {
			want(c.BookID)
		}
	}
	refs, err := h.units.BookRefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]adoptionItem, len(units))
	for i := range units {
		items[i] = toAdoptionItem(&units[i], refs)
		if h.scanner != nil {
			items[i].RootFormat = h.scanner.RootFormat(units[i].RootPath)
		}
	}
	return items, nil
}

func toAdoptionItem(u *db.UnmatchedUnit, refs map[int64]db.UnmatchedBookRef) adoptionItem {
	it := adoptionItem{
		ID: u.ID, Kind: u.UnitKind, Format: u.Format, FileCount: u.FileCount, SizeBytes: u.SizeBytes,
		RelPath: u.RelPath, RootPath: u.RootPath, AuthorFolder: u.AuthorFolder,
		ParsedTitle: u.ParsedTitle, ParsedAuthor: u.ParsedAuthor, Reason: u.Reason,
		Candidates: []adoptionCandidate{}, TopScore: u.TopScore, State: u.State,
		BookCreated: u.CreatedBookID > 0, AuthorCreated: u.CreatedAuthorID > 0,
		Members: memberNames(u), FirstSeenAt: u.FirstSeenAt, ResolvedAt: u.ResolvedAt,
	}
	// The scan decides a conflict with every author tag in hand and records
	// it; deriving it here from the parsed author would flag a narrator in
	// the Artist tag as another author (#2942).
	if u.FilesAuthor != "" && u.AuthorFolder != "" {
		it.AuthorConflict = &adoptionAuthorConflict{Files: u.FilesAuthor, Folder: u.AuthorFolder}
	}
	for _, c := range u.Candidates {
		// A suggested book deleted since the scan simply drops out.
		if ref, ok := refs[c.BookID]; ok {
			it.Candidates = append(it.Candidates, adoptionCandidate{Book: ref, Score: c.Score, FolderAuthorOnly: c.FolderAuthorOnly && it.AuthorConflict != nil})
		}
	}
	if ref, ok := refs[u.BookID]; ok && u.BookID > 0 {
		it.Book = &ref
	}
	return it
}

// memberNames lists up to maxAdoptionMembersShown member files, relative to
// the unit folder, or by base name for a file unit.
func memberNames(u *db.UnmatchedUnit) []string {
	n := min(len(u.MemberPaths), maxAdoptionMembersShown)
	out := make([]string, 0, n)
	for _, p := range u.MemberPaths[:n] {
		name := filepath.Base(p)
		if u.UnitKind == db.UnmatchedKindFolder {
			if rel, err := filepath.Rel(u.UnitPath, p); err == nil {
				name = filepath.ToSlash(rel)
			}
		}
		out = append(out, name)
	}
	return out
}

// scanStatus reads the last scan result blob and the live running flag.
func (h *AdoptionHandler) scanStatus(ctx context.Context) adoptionScanStatus {
	st := adoptionScanStatus{}
	if h.scanner != nil {
		st.Running = h.scanner.ScanRunning()
	}
	if h.settings == nil {
		return st
	}
	setting, err := h.settings.Get(ctx, SettingLibraryLastScan)
	if err != nil || setting == nil || setting.Value == "" {
		return st
	}
	var blob struct {
		RanAt          string `json:"ran_at"`
		FilesFound     int    `json:"files_found"`
		UnitsTruncated bool   `json:"units_truncated"`
		ScanError      string `json:"scan_error"`
		NoFilesFound   bool   `json:"no_files_found"`
	}
	if json.Unmarshal([]byte(setting.Value), &blob) != nil {
		return st
	}
	st.Ran = true
	st.RanAt, st.FilesFound, st.Truncated = blob.RanAt, blob.FilesFound, blob.UnitsTruncated
	st.Error, st.NoFilesFound = blob.ScanError, blob.NoFilesFound
	return st
}

// unitIDParam reads {id}, answering 400 itself when it is not a number.
func unitIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return 0, false
	}
	return id, true
}

// writeUnit answers with one row, freshly read and hydrated.
func (h *AdoptionHandler) writeUnit(w http.ResponseWriter, r *http.Request, id int64) {
	h.writeUnitWithMessage(w, r, id, "")
}

func (h *AdoptionHandler) writeUnitWithMessage(w http.ResponseWriter, r *http.Request, id int64, message string) {
	u, err := h.units.Get(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if u == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unmatched book not found"})
		return
	}
	items, err := h.hydrate(r.Context(), []db.UnmatchedUnit{*u})
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	items[0].Message = message
	writeJSON(w, http.StatusOK, items[0])
}

// stateConflict answers a compare and swap that lost, naming the state the
// row is actually in so the web can show something truthful.
func (h *AdoptionHandler) stateConflict(w http.ResponseWriter, r *http.Request, id int64) {
	u, err := h.units.Get(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if u == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unmatched book not found"})
		return
	}
	msg := map[string]string{
		db.UnmatchedStatePending:  "This book is still waiting for a decision.",
		db.UnmatchedStateAdopting: "Someone is adopting this book right now.",
		db.UnmatchedStateAdopted:  "This book was already adopted.",
		db.UnmatchedStateUndoing:  "Someone is undoing this adoption right now.",
		db.UnmatchedStateIgnored:  "This book is ignored.",
	}[u.State]
	writeJSON(w, http.StatusConflict, map[string]string{"error": msg, "state": u.State})
}

// Ignore handles POST /library/unmatched/{id}/ignore.
func (h *AdoptionHandler) Ignore(w http.ResponseWriter, r *http.Request) {
	h.moveState(w, r, db.UnmatchedStatePending, db.UnmatchedStateIgnored)
}

// Unignore handles POST /library/unmatched/{id}/unignore.
func (h *AdoptionHandler) Unignore(w http.ResponseWriter, r *http.Request) {
	h.moveState(w, r, db.UnmatchedStateIgnored, db.UnmatchedStatePending)
}

func (h *AdoptionHandler) moveState(w http.ResponseWriter, r *http.Request, from, to string) {
	id, ok := unitIDParam(w, r)
	if !ok {
		return
	}
	moved, err := h.units.ClaimState(r.Context(), id, from, to)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !moved {
		h.stateConflict(w, r, id)
		return
	}
	h.writeUnit(w, r, id)
}

// maxBulkIgnoreIDs bounds one bulk ignore by id.
const maxBulkIgnoreIDs = 500

// IgnoreMany handles POST /library/unmatched/ignore with {ids} or
// {authorFolder}. Only pending rows move.
func (h *AdoptionHandler) IgnoreMany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs          []int64 `json:"ids"`
		AuthorFolder string  `json:"authorFolder"`
	}
	if err := decodeAdoptionBody(w, r, 64<<10, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.AuthorFolder = strings.TrimSpace(req.AuthorFolder)
	if (len(req.IDs) == 0) == (req.AuthorFolder == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send either ids or authorFolder"})
		return
	}
	if len(req.IDs) > maxBulkIgnoreIDs {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too many ids"})
		return
	}
	n, err := h.units.IgnorePending(r.Context(), req.IDs, req.AuthorFolder)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"ignored": n})
}

// decodeAdoptionBody reads a bounded JSON body and refuses any field the
// request type does not declare. An adoption route never takes a path, and a
// client sending one ("path": "/etc/passwd") should learn that it was not
// used rather than have it silently dropped.
func decodeAdoptionBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return fmt.Errorf("unknown field %s", strings.TrimPrefix(err.Error(), "json: unknown field "))
		}
		return errors.New("invalid request body")
	}
	return nil
}

// errAdoptionRefused carries a status and sentence back to the handler.
type errAdoptionRefused struct {
	status int
	msg    string
}

func (e *errAdoptionRefused) Error() string { return e.msg }

func refuse(status int, msg string) error { return &errAdoptionRefused{status: status, msg: msg} }

// writeAdoptionError answers any error an adopt or undo returned.
func (h *AdoptionHandler) writeAdoptionError(w http.ResponseWriter, r *http.Request, err error) {
	var refused *errAdoptionRefused
	if errors.As(err, &refused) {
		writeJSON(w, refused.status, map[string]string{"error": refused.msg})
		return
	}
	var unavailable *primaryProviderUnavailableError
	if errors.As(err, &unavailable) {
		writePrimaryProviderUnavailable(w, unavailable.Primary)
		return
	}
	var lookup *addBookLookupError
	if errors.As(err, &lookup) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": lookup.Err.Error()})
		return
	}
	for _, resp := range addBookErrorResponses {
		if errors.Is(err, resp.err) {
			writeJSON(w, resp.status, map[string]string{"error": resp.body})
			return
		}
	}
	writeServerError(w, r, err)
}
