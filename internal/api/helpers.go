package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// BookSearcher triggers an immediate indexer search and auto-grab for a
// single wanted book. Implemented by *scheduler.Scheduler.
type BookSearcher interface {
	SearchAndGrabBook(ctx context.Context, book models.Book)
}

// BookMetaLookup fetches a single book record from a named provider,
// bypassing the TTL cache. Implemented by *metadata.Aggregator; kept as an
// interface so the Rebind handler can be tested without a real HTTP client.
type BookMetaLookup interface {
	GetBookFromProvider(ctx context.Context, provider, foreignID string) (*models.Book, error)
}

// LibraryFinder checks whether a book already exists in the local library.
// Implemented by *importer.Scanner; a nil implementation is a no-op. The
// mediaType argument selects which library roots are searched (ebook vs
// audiobook vs both) so a same-titled file in the wrong root cannot be
// mis-attributed to a book of the opposite media type.
type LibraryFinder interface {
	FindExisting(ctx context.Context, title, authorName, mediaType string) string
}

// librarySnapshotter is the optional capability behind LibraryFinder: a finder
// that can hand out a snapshot answering many FindExisting queries from one
// walk of each library root. The author sync asserts for it before its create
// loop, because that loop calls FindExisting once per new book and each call
// used to re-walk the entire library — 65 full walks for a 65-book author,
// which on network storage is the right order of magnitude for the reported
// hour-long refresh (#1888, #1929). Implemented by *importer.Scanner; a finder
// without the capability keeps its per-call behaviour.
type librarySnapshotter interface {
	SnapshotFinder() *importer.LibrarySnapshot
}

// snapshotFinder returns a batch-friendly view of finder when it offers one,
// and finder itself otherwise (including nil, which callers already treat as a
// no-op).
func snapshotFinder(finder LibraryFinder) LibraryFinder {
	if sn, ok := finder.(librarySnapshotter); ok {
		return sn.SnapshotFinder()
	}
	return finder
}

// parseID extracts the `{id}` URL parameter as an int64. If the value is
// missing or non-numeric it writes HTTP 400 and returns (0, false). Callers
// should check ok and bail out on false.
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return 0, false
	}
	return id, true
}

// Tier-1 cross-user IDOR guard (D1). loadOwnedAuthor and loadOwnedBook below
// are the one implementation of the parse, load, 404-on-missing,
// 404-on-not-mine sequence that ~48 handlers used to carry inline. 404 rather
// than 403 on an ownership mismatch is deliberate: a non-owner must not be
// able to probe for the existence of another user's rows by status code.
//
// Keeping it in one function is the point. Pasted into every handler, the
// guard holds only as long as everyone remembers to paste it, and a new
// handler that forgets is a silent cross-user read that nothing in the build
// or the linter notices (#2364).
//
// A store error is a 500, not a 404. Some of the converted sites collapsed
// `err != nil || row == nil` into 404, which reports a database failure as a
// missing row; a failure to read is not an answer about existence, and it is
// not one a non-owner can distinguish either way.

// loadOwnedAuthor reads the {id} URL param, loads the author, and enforces the
// guard above. It returns (author, true) only when the caller may proceed; on
// false the response has already been written.
func (h *AuthorHandler) loadOwnedAuthor(w http.ResponseWriter, r *http.Request) (*models.Author, bool) {
	id, ok := parseID(w, r)
	if !ok {
		return nil, false
	}
	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return nil, false
	}
	if author == nil || !auth.CheckOwnership(r.Context(), author.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return nil, false
	}
	return author, true
}

// loadOwnedBook is loadOwnedAuthor for books. Same contract, same reasoning.
func (h *BookHandler) loadOwnedBook(w http.ResponseWriter, r *http.Request) (*models.Book, bool) {
	id, ok := parseID(w, r)
	if !ok {
		return nil, false
	}
	book, err := h.books.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return nil, false
	}
	if book == nil || !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return nil, false
	}
	return book, true
}

func parseLimitOffset(r *http.Request, defaultLimit, maxLimit int) (int, int) {
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if maxLimit > 0 && limit > maxLimit {
		limit = maxLimit
	}
	if limit <= 0 {
		limit = defaultLimit
	}

	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
}

func sortName(name string) string {
	return textutil.SortName(name)
}
