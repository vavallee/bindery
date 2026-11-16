package api

import (
	"context"

	"github.com/vavallee/bindery/internal/models"
)

// sharesProviderID reports whether two author rows answer to any one provider
// id, primary or secondary. A lookup error counts as shared, so a failing read
// leaves the book where it is.
func (h *AuthorHandler) sharesProviderID(ctx context.Context, a, b *models.Author) bool {
	held := map[string]struct{}{}
	add := func(id string) {
		if id != "" {
			held[id] = struct{}{}
		}
	}
	add(a.ForeignID)
	aIDs, err := h.authors.ListAuthorIdentifiers(ctx, a.ID)
	if err != nil {
		return true
	}
	for _, i := range aIDs {
		add(i.ForeignID)
	}
	if _, ok := held[b.ForeignID]; ok {
		return true
	}
	bIDs, err := h.authors.ListAuthorIdentifiers(ctx, b.ID)
	if err != nil {
		return true
	}
	for _, i := range bIDs {
		if _, ok := held[i.ForeignID]; ok {
			return true
		}
	}
	return false
}
