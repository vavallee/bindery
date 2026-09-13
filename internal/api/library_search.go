package api

import (
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// LibrarySearchHandler backs the header search box (#2551): one query against
// the caller's own catalogue, grouped by entity, small enough to render as a
// typeahead. It never touches a metadata provider or an indexer; the "add"
// escape hatch the UI appends is a client-side handoff to the Add Book modal.
type LibrarySearchHandler struct {
	authors *db.AuthorRepo
	books   *db.BookRepo
	series  *db.SeriesRepo
}

func NewLibrarySearchHandler(authors *db.AuthorRepo, books *db.BookRepo, series *db.SeriesRepo) *LibrarySearchHandler {
	return &LibrarySearchHandler{authors: authors, books: books, series: series}
}

const (
	librarySearchDefaultLimit = 5
	librarySearchMaxLimit     = 10
)

// The rows are deliberately narrow: the dropdown shows a name, a cover and a
// link, and the full Author / Book shapes carry descriptions and joins that
// would make a per-keystroke response several times the size for nothing.
type librarySearchAuthor struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ImageURL string `json:"imageUrl,omitempty"`
}

type librarySearchBook struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	AuthorID   int64  `json:"authorId"`
	AuthorName string `json:"authorName,omitempty"`
	ImageURL   string `json:"imageUrl,omitempty"`
}

type librarySearchSeries struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type librarySearchResponse struct {
	Authors []librarySearchAuthor `json:"authors"`
	Books   []librarySearchBook   `json:"books"`
	Series  []librarySearchSeries `json:"series"`
}

// Search handles GET /search/library?q=…&limit=…. Authors match on name or
// alias, books on title or author name, series on title, all through the same
// folded search_key matching the Authors and Books pages use, so a hit here is
// a hit there. Scoped with auth.ListScopeUserID exactly like those lists:
// admins and no-tenancy callers see the shared library, everyone else sees
// their own rows plus unowned ones.
func (h *LibrarySearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q is required"})
		return
	}
	limit, _ := parseLimitOffset(r, librarySearchDefaultLimit, librarySearchMaxLimit)
	userID := auth.ListScopeUserID(ctx)

	authors, _, err := h.authors.ListPageFiltered(ctx, db.AuthorListFilter{UserID: userID, Search: q}, limit, 0)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	books, _, err := h.books.ListPageFiltered(ctx, db.BookListFilter{UserID: userID, Search: q}, limit, 0)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	series, err := h.series.SearchTitles(ctx, q, userID, limit)
	if err != nil {
		writeServerError(w, r, err)
		return
	}

	resp := librarySearchResponse{
		Authors: make([]librarySearchAuthor, 0, len(authors)),
		Books:   make([]librarySearchBook, 0, len(books)),
		Series:  make([]librarySearchSeries, 0, len(series)),
	}
	for _, a := range authors {
		resp.Authors = append(resp.Authors, librarySearchAuthor{
			ID:       a.ID,
			Name:     a.Name,
			ImageURL: ProxyImageURL(a.ImageURL),
		})
	}
	for _, b := range books {
		resp.Books = append(resp.Books, newLibrarySearchBook(b))
	}
	for _, s := range series {
		resp.Series = append(resp.Series, librarySearchSeries{ID: s.ID, Title: s.Title})
	}
	writeJSON(w, http.StatusOK, resp)
}

func newLibrarySearchBook(b models.Book) librarySearchBook {
	row := librarySearchBook{
		ID:       b.ID,
		Title:    b.Title,
		AuthorID: b.AuthorID,
		ImageURL: ProxyImageURL(b.ImageURL),
	}
	if b.Author != nil {
		row.AuthorName = b.Author.Name
	}
	return row
}
