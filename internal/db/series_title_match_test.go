package db

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestSeriesAddAlias(t *testing.T) {
	cases := []struct {
		name      string
		foreignID string
		// setup runs after the target series (and a second series) exist.
		setup     func(t *testing.T, repo *SeriesRepo, target, other int64)
		seriesID  func(target, other int64) int64
		wantAdded bool
		wantErr   bool
		// wantResolves is the series the id names afterwards, 0 for none.
		wantResolves func(target, other int64) int64
	}{
		{
			name:         "records a new id on the series",
			foreignID:    "calibre:series:cradle",
			seriesID:     func(target, _ int64) int64 { return target },
			wantAdded:    true,
			wantResolves: func(target, _ int64) int64 { return target },
		},
		{
			name:         "an id a live series carries stays that series",
			foreignID:    "abs:series:lib1:other",
			seriesID:     func(target, _ int64) int64 { return target },
			wantResolves: func(_, other int64) int64 { return other },
		},
		{
			name:      "an existing alias keeps the series it names",
			foreignID: "calibre:series:cradle",
			setup: func(t *testing.T, repo *SeriesRepo, _, other int64) {
				addSeriesAlias(t, repo, "calibre:series:cradle", other)
			},
			seriesID:     func(target, _ int64) int64 { return target },
			wantResolves: func(_, other int64) int64 { return other },
		},
		{
			name:         "a series that does not exist gets nothing",
			foreignID:    "calibre:series:cradle",
			seriesID:     func(_, _ int64) int64 { return 9999 },
			wantResolves: func(_, _ int64) int64 { return 0 },
		},
		{
			name:         "an empty id is an error",
			foreignID:    "  ",
			seriesID:     func(target, _ int64) int64 { return target },
			wantErr:      true,
			wantResolves: func(_, _ int64) int64 { return 0 },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database, err := OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			ctx := context.Background()
			repo := NewSeriesRepo(database)
			target := &models.Series{ForeignID: "abs:series:lib1:cradle", Title: "Cradle"}
			other := &models.Series{ForeignID: "abs:series:lib1:other", Title: "Cradle"}
			for _, s := range []*models.Series{target, other} {
				if err := repo.CreateOrGet(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
			if tc.setup != nil {
				tc.setup(t, repo, target.ID, other.ID)
			}

			added, err := repo.AddAlias(ctx, tc.foreignID, tc.seriesID(target.ID, other.ID))
			if (err != nil) != tc.wantErr {
				t.Fatalf("AddAlias err = %v, wantErr %v", err, tc.wantErr)
			}
			if added != tc.wantAdded {
				t.Errorf("AddAlias added = %v, want %v", added, tc.wantAdded)
			}
			got, err := repo.GetByForeignID(ctx, tc.foreignID)
			if err != nil {
				t.Fatal(err)
			}
			var gotID int64
			if got != nil {
				gotID = got.ID
			}
			if want := tc.wantResolves(target.ID, other.ID); gotID != want {
				t.Errorf("%q resolves to series %d, want %d", tc.foreignID, gotID, want)
			}
		})
	}
}

// An alias AddAlias wrote is honoured by the paths that guard aliases: a
// CreateOrGet with the id lands on the series, and Create refuses it.
func TestSeriesAddAliasBlocksRecreate(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewSeriesRepo(database)
	target := &models.Series{ForeignID: "abs:series:lib1:cradle", Title: "Cradle"}
	if err := repo.CreateOrGet(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddAlias(ctx, "calibre:series:cradle", target.ID); err != nil {
		t.Fatal(err)
	}
	again := &models.Series{ForeignID: "calibre:series:cradle", Title: "Cradle"}
	if err := repo.CreateOrGet(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != target.ID {
		t.Errorf("CreateOrGet(alias) = series %d, want %d", again.ID, target.ID)
	}
	if err := repo.Create(ctx, &models.Series{ForeignID: "calibre:series:cradle", Title: "Cradle"}); !errors.Is(err, ErrSeriesAlias) {
		t.Errorf("Create(alias) err = %v, want ErrSeriesAlias", err)
	}
}

func TestSeriesAuthorEvidence(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	series := NewSeriesRepo(database)
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	mkAuthor := func(fid, name string) int64 {
		a := &models.Author{ForeignID: fid, Name: name, SortName: name}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	asimov := mkAuthor("OL1A", "Isaac Asimov")
	other := mkAuthor("OL2A", "Someone Else")
	mkSeries := func(fid string, bookAuthors ...int64) int64 {
		s := &models.Series{ForeignID: fid, Title: "Foundation"}
		if err := series.CreateOrGet(ctx, s); err != nil {
			t.Fatal(err)
		}
		for n, aid := range bookAuthors {
			b := &models.Book{ForeignID: fid + ":" + string(rune('a'+n)), AuthorID: aid, Title: fid + string(rune('a'+n)), Status: models.BookStatusWanted}
			if err := books.Create(ctx, b); err != nil {
				t.Fatal(err)
			}
			if err := series.LinkBook(ctx, s.ID, b.ID, "", true); err != nil {
				t.Fatal(err)
			}
		}
		return s.ID
	}
	cases := []struct {
		name               string
		seriesID           int64
		author             int64
		wantBooks, wantHit int
	}{
		{"empty series", mkSeries("s:empty"), asimov, 0, 0},
		{"only the author's books", mkSeries("s:asimov", asimov, asimov), asimov, 2, 2},
		{"only another author's books", mkSeries("s:other", other), asimov, 1, 0},
		{"mixed", mkSeries("s:mixed", other, asimov), asimov, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := series.SeriesAuthorEvidence(ctx, tc.seriesID, tc.author)
			if err != nil {
				t.Fatal(err)
			}
			if !ev.Exists || ev.Books != tc.wantBooks || ev.ByAuthor != tc.wantHit {
				t.Errorf("SeriesAuthorEvidence = %+v; want existing, %d books, %d by author", ev, tc.wantBooks, tc.wantHit)
			}
			if ev.AuthorName != "Isaac Asimov" || ev.HardcoverAuthorName != "" {
				t.Errorf("SeriesAuthorEvidence names = %q / %q, want the author's name and no Hardcover author", ev.AuthorName, ev.HardcoverAuthorName)
			}
		})
	}

	// A Hardcover link's author comes back with the evidence.
	linked := mkSeries("s:linked")
	if err := series.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID: linked, HardcoverSeriesID: "hc-series:42", HardcoverTitle: "Foundation", HardcoverAuthorName: "Isaac Asimov",
	}); err != nil {
		t.Fatal(err)
	}
	ev, err := series.SeriesAuthorEvidence(ctx, linked, other)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Exists || ev.Books != 0 || ev.HardcoverAuthorName != "Isaac Asimov" || ev.AuthorName != "Someone Else" {
		t.Errorf("linked series evidence = %+v", ev)
	}
	// A series that is gone reads as not existing, not as empty.
	if err := series.Delete(ctx, linked); err != nil {
		t.Fatal(err)
	}
	ev, err = series.SeriesAuthorEvidence(ctx, linked, asimov)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Exists || ev.Books != 0 {
		t.Errorf("deleted series evidence = %+v, want not existing", ev)
	}

	titles, err := series.ListTitles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(titles) != len(cases) || titles[0].Title != "Foundation" || titles[0].ForeignID != "s:empty" {
		t.Errorf("ListTitles = %+v, want the %d series in id order", titles, len(cases))
	}
}
