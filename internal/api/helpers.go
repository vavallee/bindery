package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/models"
)

// BookSearcher triggers an immediate indexer search and auto-grab for a
// single wanted book. Implemented by *scheduler.Scheduler.
type BookSearcher interface {
	SearchAndGrabBook(ctx context.Context, book models.Book)
}

// LibraryFinder checks whether a book already exists in the local library.
// Implemented by *importer.Scanner; a nil implementation is a no-op. The
// mediaType argument selects which library roots are searched (ebook vs
// audiobook vs both) so a same-titled file in the wrong root cannot be
// mis-attributed to a book of the opposite media type.
type LibraryFinder interface {
	FindExisting(ctx context.Context, title, authorName, mediaType string) string
}

func contextBackground() context.Context {
	return context.Background()
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
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}
