package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestSearchAndGrabFormat_StampsTheGrabbedMediaType (#1964): the auto grab
// records the slot it filled on the download row.
func TestSearchAndGrabFormat_StampsTheGrabbedMediaType(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()

	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

	rows, err := f.downloads.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected one download row, got %d (%v)", len(rows), err)
	}
	if rows[0].MediaType != models.MediaTypeEbook {
		t.Errorf("stored media type = %q, want %q", rows[0].MediaType, models.MediaTypeEbook)
	}
}
