package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type SearchHandler struct {
	meta *metadata.Aggregator
	// books and authors are consulted to stamp each metadata result with the
	// library row it already corresponds to (#1227). Either may be nil, in
	// which case results are returned unstamped.
	books   *db.BookRepo
	authors *db.AuthorRepo
}

// bookSearchResult exposes transient provider identifiers without adding them
// to every Book response. ProviderISBNs exists only while metadata is being
// searched; persisted books load their identifiers from the editions table.
//
// LibraryBookID is set when the result's foreign id already resolves to a book
// the requesting user owns, so the client can offer "open" instead of "add"
// (#1227). It is the book row's id, distinct from the embedded Book.ID, which
// is the provider's transient value (usually 0) on a search result.
type bookSearchResult struct {
	models.Book
	ISBNs         []string `json:"isbns,omitempty"`
	LibraryBookID *int64   `json:"libraryBookId,omitempty"`
}

// authorSearchResult is the author counterpart of bookSearchResult.
// LibraryAuthorID is set when the result's foreign id matches a library author
// visible to the requesting user, by primary id or alternate identifier.
type authorSearchResult struct {
	models.Author
	LibraryAuthorID *int64 `json:"libraryAuthorId,omitempty"`
}

// stampLibraryBooks fills LibraryBookID on every result whose foreign id the
// requesting user already owns, with one query for the whole list. A lookup
// failure is logged and leaves the results unstamped: the upstream search
// succeeded and that is what the caller asked for.
func (h *SearchHandler) stampLibraryBooks(ctx context.Context, results []bookSearchResult) {
	if h.books == nil || len(results) == 0 {
		return
	}
	ids := make([]string, 0, len(results))
	for i := range results {
		if results[i].ForeignID != "" {
			ids = append(ids, results[i].ForeignID)
		}
	}
	found, err := h.books.LibraryIDsByForeignIDsForUser(ctx, ids, auth.ListScopeUserID(ctx))
	if err != nil {
		slog.Warn("search: library book lookup failed, results left unstamped", "error", err)
		return
	}
	for i := range results {
		if id, ok := found[results[i].ForeignID]; ok {
			libraryID := id
			results[i].LibraryBookID = &libraryID
		}
	}
}

// stampLibraryAuthors is stampLibraryBooks for author results.
func (h *SearchHandler) stampLibraryAuthors(ctx context.Context, results []authorSearchResult) {
	if h.authors == nil || len(results) == 0 {
		return
	}
	ids := make([]string, 0, len(results))
	for i := range results {
		if results[i].ForeignID != "" {
			ids = append(ids, results[i].ForeignID)
		}
	}
	found, err := h.authors.LibraryIDsByAnyForeignIDsForUser(ctx, ids, auth.ListScopeUserID(ctx))
	if err != nil {
		slog.Warn("search: library author lookup failed, results left unstamped", "error", err)
		return
	}
	for i := range results {
		if id, ok := found[strings.TrimSpace(results[i].ForeignID)]; ok {
			libraryID := id
			results[i].LibraryAuthorID = &libraryID
		}
	}
}

const maxBookSearchISBNs = 100

func newBookSearchResult(book models.Book) bookSearchResult {
	capacity := min(maxBookSearchISBNs, len(book.ProviderISBNs)+len(book.Editions)*2)
	isbns := make([]string, 0, capacity)
	seen := make(map[string]bool, capacity)
	add := func(raw string) {
		if len(isbns) >= maxBookSearchISBNs {
			return
		}
		normalized := isbnutil.Normalize(raw)
		if isbnutil.ToISBN13(normalized) == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		isbns = append(isbns, normalized)
	}

	for _, isbn := range book.ProviderISBNs {
		add(isbn)
	}
	for _, edition := range book.Editions {
		if edition.ISBN13 != nil {
			add(*edition.ISBN13)
		}
		if edition.ISBN10 != nil {
			add(*edition.ISBN10)
		}
	}

	return bookSearchResult{Book: book, ISBNs: isbns}
}

// NewSearchHandler wires the metadata aggregator plus the library repos used to
// mark results that are already in the requesting user's library. books and
// authors may be nil (tests, or callers that do not want stamping).
func NewSearchHandler(meta *metadata.Aggregator, books *db.BookRepo, authors *db.AuthorRepo) *SearchHandler {
	return &SearchHandler{meta: meta, books: books, authors: authors}
}

// writeUpstreamError responds with 502 Bad Gateway and a message that makes
// it obvious the failure is on the metadata provider side (OpenLibrary,
// Google Books, Hardcover), not inside Bindery. Using 500 for this conflates
// provider outages with real server bugs and trains users to ignore 500s.
//
// The client-facing message is intentionally generic and never includes the
// underlying err string. Transport errors from the metadata clients wrap a
// *url.Error whose Error() embeds the full upstream request URL, and the
// Google Books URL carries the API key (?key=...) in the query string plus the
// internal DNS resolver IP. Echoing err.Error() back to the caller therefore
// leaked the API key and internal infra (#1144). The full error is still
// logged server-side so operators keep the detail for debugging.
func writeUpstreamError(w http.ResponseWriter, err error) {
	slog.Warn("metadata provider request failed", "error", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{
		"error": "metadata provider unavailable",
	})
}

func (h *SearchHandler) SearchAuthors(w http.ResponseWriter, r *http.Request) {
	term := r.URL.Query().Get("term")
	if term == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "term parameter required"})
		return
	}

	authors, err := h.meta.SearchAuthors(r.Context(), term)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	results := make([]authorSearchResult, len(authors))
	for i := range authors {
		results[i] = authorSearchResult{Author: authors[i]}
	}
	h.stampLibraryAuthors(r.Context(), results)
	writeJSON(w, http.StatusOK, results)
}

func (h *SearchHandler) SearchBooks(w http.ResponseWriter, r *http.Request) {
	term := r.URL.Query().Get("term")
	if term == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "term parameter required"})
		return
	}

	books, err := h.meta.SearchBooks(r.Context(), term)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	results := make([]bookSearchResult, len(books))
	for i := range books {
		results[i] = newBookSearchResult(books[i])
	}
	h.stampLibraryBooks(r.Context(), results)
	writeJSON(w, http.StatusOK, results)
}

// lookupResult wraps a single lookup hit the same way SearchBooks wraps a
// list, including the library stamp.
func (h *SearchHandler) lookupResult(ctx context.Context, book models.Book) bookSearchResult {
	results := []bookSearchResult{newBookSearchResult(book)}
	h.stampLibraryBooks(ctx, results)
	return results[0]
}

// Lookup resolves a single book by a stable identifier passed as a query
// param: either `isbn` (the original behavior) or `asin` (an Audible/audiobook
// identifier). The route is shared (`/book/lookup`) so existing ISBN callers
// keep working unchanged.
func (h *SearchHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	asin := strings.TrimSpace(r.URL.Query().Get("asin"))
	isbn := r.URL.Query().Get("isbn")

	switch {
	case asin != "":
		h.lookupByASIN(w, r, asin)
	case isbn != "":
		h.lookupByISBN(w, r, isbn)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "isbn or asin parameter required"})
	}
}

func (h *SearchHandler) lookupByISBN(w http.ResponseWriter, r *http.Request, isbn string) {
	book, err := h.meta.GetBookByISBN(r.Context(), isbn)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("No book found for ISBN %s. Check the number, or try searching by title instead.", isbn),
		})
		return
	}

	writeJSON(w, http.StatusOK, h.lookupResult(r.Context(), *book))
}

func (h *SearchHandler) lookupByASIN(w http.ResponseWriter, r *http.Request, asin string) {
	book, err := h.meta.GetCanonicalBookByASIN(r.Context(), asin)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("No book found for ASIN %s. Check the identifier, or try searching by title instead.", asin),
		})
		return
	}

	// The resolver canonicalizes the ASIN against the primary provider, so the
	// returned book carries the canonical foreignBookId (keep it) but loses the
	// ASIN-origin shape. Re-stamp the ASIN and audiobook media type so the Add
	// Book modal renders it as the audiobook edition the user searched for.
	if book.ASIN == "" {
		book.ASIN = asin
	}
	book.MediaType = models.MediaTypeAudiobook

	writeJSON(w, http.StatusOK, h.lookupResult(r.Context(), *book))
}

// writeServerError logs the underlying error server-side (with request
// context) and returns a generic 500 body, so internal details like SQL
// text or filesystem paths never reach the client.
func writeServerError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Warn("failed to encode JSON response", "status", status, "error", err)
	}
}
