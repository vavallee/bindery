package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// What makes a merge stick (#2554): a refresh that still reports a merged-away
// series id files the book under the series it was merged into, and does not
// recreate the old series.
func TestExistingBookSeriesLinker_FollowsMergeAliases(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	kept := f.addImportedBook(t, "OL1W", "Fjellvinden")
	later := f.addImportedBook(t, "OL2W", "Havets stemme")

	target := &models.Series{ForeignID: "hc-series:900", Title: "Fjellserien"}
	source := &models.Series{ForeignID: "ol-series:fjellserien-trilogien", Title: "Fjellserien-trilogien"}
	for _, s := range []*models.Series{target, source} {
		if err := f.series.CreateOrGet(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.series.LinkBook(ctx, source.ID, kept.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.Merge(ctx, target.ID, []int64{source.ID}, ""); err != nil {
		t.Fatal(err)
	}

	linker := newExistingBookSeriesLinker(f.series, f.author.ID, map[int64]struct{}{kept.ID: {}, later.ID: {}})
	for _, b := range []*models.Book{kept, later} {
		pos := map[int64]string{kept.ID: "1", later.ID: "2"}[b.ID]
		linker.link(ctx, b, []models.SeriesRef{{ForeignID: source.ForeignID, Title: source.Title, Position: pos, Primary: true}})
	}

	for b, want := range map[*models.Book]string{kept: "1", later: "2"} {
		links := linksForBook(t, f, b.ID)
		if len(links) != 1 || links[0].seriesTitle != "Fjellserien" || links[0].position != want {
			t.Errorf("%s links = %+v, want only Fjellserien at %s", b.Title, links, want)
		}
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM series`).Scan(&n); err != nil || n != 1 {
		t.Errorf("series rows = %d (%v), want 1: the refresh recreated the merged series", n, err)
	}
}

// In series monitor mode a new book is monitored when the provider reports it
// in a pinned series. Reported under an id merged into the pinned series
// (#2554), it is in that series too, so it is monitored at creation as well.
func TestCatalogueSync_MonitorsBooksReportedUnderAMergedSeriesID(t *testing.T) {
	f := newSeriesLinkFixture(t, true)
	ctx := context.Background()
	f.author.MonitorMode = models.AuthorMonitorModeSeries
	if err := f.authors.Update(ctx, f.author); err != nil {
		t.Fatal(err)
	}
	first := f.addImportedBook(t, "OL1W", "Fjellvinden")
	target := &models.Series{ForeignID: "hc-series:900", Title: "Fjellserien"}
	source := &models.Series{ForeignID: "ol-series:fjellserien-trilogien", Title: "Fjellserien-trilogien"}
	for _, s := range []*models.Series{target, source} {
		if err := f.series.CreateOrGet(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.series.LinkBook(ctx, target.ID, first.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.Merge(ctx, target.ID, []int64{source.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.authors.SetMonitoredSeriesIDs(ctx, f.author.ID, []int64{target.ID}); err != nil {
		t.Fatal(err)
	}

	later := seriesWork("OL2W", "Havets stemme", "2")
	later.SeriesRefs[0].ForeignID, later.SeriesRefs[0].Title = source.ForeignID, source.Title
	refreshCatalogue(t, f.handler(&stubMetaProvider{works: []models.Book{later}}), f.author)

	got, err := f.books.GetByForeignID(ctx, "OL2W")
	if err != nil || got == nil {
		t.Fatalf("new book not created: %v", err)
	}
	if !got.Monitored {
		t.Error("a new book in the pinned series, reported under a merged-away id, was not monitored")
	}
	if links := linksForBook(t, f, got.ID); len(links) != 1 || links[0].seriesTitle != "Fjellserien" {
		t.Errorf("links = %+v, want only Fjellserien", links)
	}
}

// A book the user takes out of a series stays out: a refresh that still
// reports it in that series does not put it back (#2554).
func TestCatalogueSync_KeepsABookTheUserRemovedFromASeriesOut(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	book := f.addImportedBook(t, "OL1W", "Fjellvinden")
	s := &models.Series{ForeignID: "OL-S-IMPERIAL-RADCH", Title: "Fjellserien"}
	if err := f.series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := f.series.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	h := NewSeriesHandler(f.series, f.books, f.authors, nil, nil)
	id := strconv.FormatInt(s.ID, 10)
	rec := httptest.NewRecorder()
	h.RemoveBook(rec, withURLParams(httptest.NewRequest(http.MethodDelete, "/api/v1/series/"+id+"/books/"+strconv.FormatInt(book.ID, 10), nil),
		map[string]string{"id": id, "bookId": strconv.FormatInt(book.ID, 10)}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}

	// seriesWork reports the book in that series again.
	refreshCatalogue(t, f.handler(&stubMetaProvider{works: []models.Book{seriesWork("OL1W", "Fjellvinden", "1")}}), f.author)
	if links := linksForBook(t, f, book.ID); len(links) != 0 {
		t.Errorf("links = %+v, want the book kept out of the series it was removed from", links)
	}
}
