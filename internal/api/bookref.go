package api

import (
	"context"

	"github.com/vavallee/bindery/internal/db"
)

// bookRef is the minimal book + author projection attached to queue, pending,
// and history items so the web UI can render the book title and author name as
// links to /book/:id and /author/:id. Kept deliberately small — the rows these
// hang off are hot (the queue polls every 5s), and the frontend only needs the
// ids and display names.
type bookRef struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	AuthorID   int64  `json:"authorId"`
	AuthorName string `json:"authorName"`
}

// loadBookRefs dedupes and drops zero/negative ids, batch-loads the matching
// books (each with its Author projection via BookRepo.GetByIDs), and returns a
// lookup of book id -> bookRef. Books that no longer exist (deleted) are simply
// absent from the map. Best-effort by contract: on a query error it returns an
// empty map plus the error so callers can still render rows without links.
func loadBookRefs(ctx context.Context, books *db.BookRepo, ids []int64) (map[int64]*bookRef, error) {
	uniq := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	refs := make(map[int64]*bookRef, len(uniq))
	if len(uniq) == 0 {
		return refs, nil
	}
	loaded, err := books.GetByIDs(ctx, uniq)
	if err != nil {
		return refs, err
	}
	for id, b := range loaded {
		ref := &bookRef{ID: b.ID, Title: b.Title, AuthorID: b.AuthorID}
		if b.Author != nil {
			ref.AuthorName = b.Author.Name
		}
		refs[id] = ref
	}
	return refs, nil
}
