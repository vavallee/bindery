package db

import (
	"context"

	"github.com/vavallee/bindery/internal/models"
)

// ListWantedMonitoredByMetadataProfile returns the monitored, wanted, not
// excluded books whose author is governed by the given metadata profile. An
// author with no profile of its own counts as governed by the seeded default.
// These are the books a profile filter turned on later could still have an
// opinion about (#2208): an imported book is already on disk and an
// unmonitored one is already out of the search.
func (r *BookRepo) ListWantedMonitoredByMetadataProfile(ctx context.Context, profileID int64) ([]models.Book, error) {
	return r.query(ctx, bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+
		` WHERE books.status = ? AND books.monitored = 1 AND books.excluded = 0
		  AND COALESCE(au.metadata_profile_id, ?) = ?
		ORDER BY au.sort_name, books.sort_key, books.sort_title`,
		[]any{models.BookStatusWanted, models.DefaultMetadataProfileID, profileID})
}
