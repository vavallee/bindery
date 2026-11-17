package db

import (
	"context"
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestSeriesGettersAgreeOnScalarColumns writes a series with every scalar
// column set and reads it back through each single-row getter. Each getter
// once carried its own hand-written column list, and GetByForeignID silently
// dropped genre_override; comparing the whole scalar row catches the next
// column a getter forgets.
func TestSeriesGettersAgreeOnScalarColumns(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewSeriesRepo(database)

	created := &models.Series{
		ForeignID:   "ol-series:read-back",
		Title:       "Read Back",
		Description: "every scalar column populated",
		Monitored:   true,
	}
	if err := repo.CreateOrGet(ctx, created); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetGenreOverride(ctx, created.ID, []string{"Fantasy", "Horror"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE series SET monitored=1 WHERE id=?", created.ID); err != nil {
		t.Fatal(err)
	}

	byID, err := repo.GetByID(ctx, created.ID)
	if err != nil || byID == nil {
		t.Fatalf("GetByID: %v, %v", byID, err)
	}
	want := scalarSeries(*byID)
	if !want.Monitored || !want.GenreOverrideSet || len(want.GenreOverride) != 2 {
		t.Fatalf("GetByID did not read back the populated row: %+v", want)
	}

	byForeignID, err := repo.GetByForeignID(ctx, created.ForeignID)
	if err != nil || byForeignID == nil {
		t.Fatalf("GetByForeignID: %v, %v", byForeignID, err)
	}
	if got := scalarSeries(*byForeignID); !reflect.DeepEqual(got, want) {
		t.Errorf("GetByForeignID scalar row = %+v\nwant %+v", got, want)
	}

	all, err := repo.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("List: %v, %v", all, err)
	}
	if got := scalarSeries(all[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("List scalar row = %+v\nwant %+v", got, want)
	}
}

// scalarSeries keeps only the fields read from the series row itself, so
// getters that hydrate different relations (books, links, monitoring) still
// compare equal on the columns they all select.
func scalarSeries(s models.Series) models.Series {
	return models.Series{
		ID:               s.ID,
		ForeignID:        s.ForeignID,
		Title:            s.Title,
		Description:      s.Description,
		Monitored:        s.Monitored,
		GenreOverride:    s.GenreOverride,
		GenreOverrideSet: s.GenreOverrideSet,
		CreatedAt:        s.CreatedAt,
	}
}

// TestSeriesGetByIDForUser_BooksQueryErrorSurfaces makes the series row
// readable but the follow-up books query fail. The repo used to return the
// series with an empty book list and a nil error, which callers render as a
// series that has no books.
func TestSeriesGetByIDForUser_BooksQueryErrorSurfaces(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewSeriesRepo(database)

	series, err := repo.CreateManual(ctx, "Broken Books")
	if err != nil {
		t.Fatal(err)
	}
	// The series query never reads books, so only the second query breaks.
	if _, err := database.ExecContext(ctx, "ALTER TABLE books RENAME COLUMN excluded TO excluded_gone"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetByIDForUser(ctx, series.ID, 0)
	if err == nil {
		t.Fatalf("GetByIDForUser returned %+v with nil error; want the books query error", got)
	}
}
