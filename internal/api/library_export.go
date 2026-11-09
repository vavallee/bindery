package api

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Export paging bounds. The cap is the contract: a client that asks for more
// gets exactly this many, never an error.
const (
	libraryExportDefaultLimit = 100
	libraryExportMaxLimit     = 200

	// libraryExportVersion is the shape version of the response. Bump it only
	// for a change a reader of the old shape would misread (a field removed,
	// renamed or given a new meaning). Adding a field does not bump it.
	libraryExportVersion = 1
)

// LibraryExportHandler serves GET /library/export, a paged read only dump of
// the library for companion tools (#3328).
type LibraryExportHandler struct {
	books    *db.BookRepo
	files    *db.BookFileRepo
	editions *db.EditionRepo
	series   *db.SeriesRepo
}

func NewLibraryExportHandler(books *db.BookRepo, files *db.BookFileRepo, editions *db.EditionRepo, series *db.SeriesRepo) *LibraryExportHandler {
	return &LibraryExportHandler{books: books, files: files, editions: editions, series: series}
}

type libraryExportResponse struct {
	Items []libraryExportBook `json:"items"`
	// NextCursor is the value to pass as ?cursor= for the next page, or empty
	// on the last page.
	NextCursor string `json:"nextCursor"`
	// Version is the shape of this response; see libraryExportVersion.
	Version int `json:"version"`
}

type libraryExportBook struct {
	ID         int64                  `json:"id"`
	Title      string                 `json:"title"`
	AuthorID   int64                  `json:"authorId"`
	AuthorName string                 `json:"authorName"`
	Files      []libraryExportFile    `json:"files"`
	Editions   []libraryExportEdition `json:"editions"`
	Series     []libraryExportSeries  `json:"series"`
}

type libraryExportFile struct {
	Path string `json:"path"`
	// Format is the file extension without the dot, lower case ("epub",
	// "m4b"); empty when the path has none.
	Format string `json:"format"`
	Size   int64  `json:"size"`
	// MediaType is "ebook" or "audiobook".
	MediaType string `json:"mediaType"`
}

type libraryExportEdition struct {
	ID        int64  `json:"id"`
	ForeignID string `json:"foreignEditionId"`
	Title     string `json:"title"`
	ISBN13    string `json:"isbn13"`
	ISBN10    string `json:"isbn10"`
	ASIN      string `json:"asin"`
	Publisher string `json:"publisher"`
	Format    string `json:"format"`
	Language  string `json:"language"`
	IsEbook   bool   `json:"isEbook"`
}

type libraryExportSeries struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Position string `json:"position"`
	Primary  bool   `json:"primary"`
}

// Export handles GET /library/export?limit=&cursor=.
//
// Books come in id order, scoped as GET /book is scoped (auth.ListScopeUserID),
// excluded books left out. cursor is the nextCursor of the previous page, and
// a page is a keyset page: nothing shifts if books are added or removed
// between calls. Each page costs four queries whatever its size.
func (h *LibraryExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	limit := libraryExportDefaultLimit
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be a positive integer"})
			return
		}
		limit = min(n, libraryExportMaxLimit)
	}
	var after int64
	if v := strings.TrimSpace(r.URL.Query().Get("cursor")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid cursor"})
			return
		}
		after = n
	}

	ctx := r.Context()
	// One extra row says whether there is a next page.
	rows, err := h.books.ListExportPage(ctx, after, limit+1, auth.ListScopeUserID(ctx))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}

	ids := make([]int64, len(rows))
	for i, b := range rows {
		ids[i] = b.ID
	}
	var (
		files    map[int64][]models.BookFile
		editions map[int64][]models.Edition
		series   map[int64][]db.BookSeriesMembership
	)
	if len(ids) > 0 {
		if files, err = h.files.ListByBooks(ctx, ids); err == nil {
			if editions, err = h.editions.ListByBooks(ctx, ids); err == nil {
				series, err = h.series.ListBookSeriesMembershipsByBooks(ctx, ids)
			}
		}
		if err != nil {
			writeServerError(w, r, err)
			return
		}
	}

	items := make([]libraryExportBook, 0, len(rows))
	for _, b := range rows {
		item := libraryExportBook{
			ID: b.ID, Title: b.Title, AuthorID: b.AuthorID, AuthorName: b.AuthorName,
			Files:    make([]libraryExportFile, 0, len(files[b.ID])),
			Editions: make([]libraryExportEdition, 0, len(editions[b.ID])),
			Series:   make([]libraryExportSeries, 0, len(series[b.ID])),
		}
		for _, f := range files[b.ID] {
			item.Files = append(item.Files, libraryExportFile{
				Path: f.Path, Format: strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Path), ".")),
				Size: f.SizeBytes, MediaType: f.Format,
			})
		}
		for _, e := range editions[b.ID] {
			item.Editions = append(item.Editions, libraryExportEdition{
				ID: e.ID, ForeignID: e.ForeignID, Title: e.Title,
				ISBN13: derefString(e.ISBN13), ISBN10: derefString(e.ISBN10), ASIN: derefString(e.ASIN),
				Publisher: e.Publisher, Format: e.Format, Language: e.Language, IsEbook: e.IsEbook,
			})
		}
		for _, m := range series[b.ID] {
			item.Series = append(item.Series, libraryExportSeries{ID: m.SeriesID, Title: m.SeriesTitle, Position: m.Position, Primary: m.Primary})
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, libraryExportResponse{Items: items, NextCursor: next, Version: libraryExportVersion})
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
