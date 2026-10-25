package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The request page's library projection promises to match like the Books page,
// so a series name finds the book there too (#3462), and the owner scope still
// applies to a series-only match.
func TestListLibraryProjectionMatchesSeriesName(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	viewer := seedUser(t, ctx, database, "proj-series-viewer")
	stranger := seedUser(t, ctx, database, "proj-series-stranger")

	a := &models.Author{ForeignID: "OL-PS-A", Name: "Charles Todd", SortName: "Todd, Charles"}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	mk := func(fid, title string, owner int64) *models.Book {
		t.Helper()
		b := &models.Book{ForeignID: fid, AuthorID: a.ID, Title: title, SortTitle: title, MediaType: models.MediaTypeEbook, OwnerUserID: owner}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	mine := mk("OL-PS-1", "A Test of Wills", viewer)
	theirs := mk("OL-PS-2", "A Duty to the Dead", stranger)
	mk("OL-PS-3", "Not In The Series", viewer)

	series := NewSeriesRepo(database)
	s := &models.Series{ForeignID: "OL-PS-S", Title: "Inspector Ian Rutledge"}
	if err := series.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	for _, b := range []*models.Book{mine, theirs} {
		if err := series.LinkBook(ctx, s.ID, b.ID, "1", true); err != nil {
			t.Fatal(err)
		}
	}

	repo := NewRequestRepo(database)
	rows, total, err := repo.ListLibraryProjection(ctx, viewer, "ian rutledge todd", 50, 0)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != mine.ID {
		t.Fatalf("scoped series search = %+v total=%d, %v; want only the viewer's own book", rows, total, err)
	}
	rows, total, err = repo.ListLibraryProjection(ctx, 0, "ian rutledge", 50, 0)
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("unscoped series search = %+v total=%d, %v; want both series books", rows, total, err)
	}
}
