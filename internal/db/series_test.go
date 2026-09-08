package db

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestSeriesCreateOrGet_Idempotent(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewSeriesRepo(database)

	s := &models.Series{
		ForeignID: "ol-series:dune-chronicles",
		Title:     "Dune Chronicles",
	}

	// First call should insert.
	if err := repo.CreateOrGet(ctx, s); err != nil {
		t.Fatalf("first CreateOrGet: %v", err)
	}
	if s.ID == 0 {
		t.Fatal("expected non-zero ID after first CreateOrGet")
	}
	firstID := s.ID

	// Second call with the same foreign_id should return the same ID.
	s2 := &models.Series{
		ForeignID: "ol-series:dune-chronicles",
		Title:     "Dune Chronicles",
	}
	if err := repo.CreateOrGet(ctx, s2); err != nil {
		t.Fatalf("second CreateOrGet: %v", err)
	}
	if s2.ID != firstID {
		t.Errorf("expected same ID %d on second call, got %d", firstID, s2.ID)
	}

	// Verify only one row exists.
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 series row, got %d", len(list))
	}
}

func TestSeriesLinkBook(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	// Seed author + book.
	author := &models.Author{
		ForeignID: "OL1A", Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL1W", AuthorID: author.ID, Title: "Dune", SortTitle: "Dune",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// Upsert series and link book.
	s := &models.Series{ForeignID: "ol-series:dune-chronicles", Title: "Dune Chronicles"}
	if err := seriesRepo.CreateOrGet(ctx, s); err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatalf("LinkBook: %v", err)
	}

	// GetByID should return the series with the book attached.
	got, err := seriesRepo.GetByID(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected series, got nil")
		return
	}
	if got.Title != "Dune Chronicles" {
		t.Errorf("title: want %q, got %q", "Dune Chronicles", got.Title)
	}
	if len(got.Books) != 1 {
		t.Fatalf("expected 1 series_book, got %d", len(got.Books))
	}
	sb := got.Books[0]
	if sb.PositionInSeries != "1" {
		t.Errorf("position: want %q, got %q", "1", sb.PositionInSeries)
	}
	if !sb.PrimarySeries {
		t.Error("expected primary_series=true")
	}
	if sb.Book == nil || sb.Book.Title != "Dune" {
		t.Errorf("expected joined book 'Dune', got %v", sb.Book)
	}

	// LinkBook is idempotent (INSERT OR IGNORE).
	if err := seriesRepo.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Errorf("second LinkBook should be idempotent, got: %v", err)
	}

	// Cascade: deleting the book should remove the series_books row.
	if err := bookRepo.Delete(ctx, book.ID); err != nil {
		t.Fatalf("delete book: %v", err)
	}
	got, err = seriesRepo.GetByID(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetByID after book delete: %v", err)
	}
	if len(got.Books) != 0 {
		t.Errorf("expected 0 series_books after book delete, got %d", len(got.Books))
	}
}

// TestSeriesListBooksExcludesHidden covers #2302: ListBooksInSeries must skip
// excluded books so series fill and genre apply do not act on them, while
// ListBooksInSeriesIncludingExcluded still returns them for callers that count
// every book in the series (e.g. import-run rollback).
func TestSeriesListBooksExcludesHidden(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	author := &models.Author{
		ForeignID: "OL-2302-A", Name: "Robert Jordan", SortName: "Jordan, Robert",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	kept := &models.Book{
		ForeignID: "OL-2302-K", AuthorID: author.ID, Title: "The Eye of the World", SortTitle: "Eye of the World",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, kept); err != nil {
		t.Fatal(err)
	}
	hidden := &models.Book{
		ForeignID: "OL-2302-H", AuthorID: author.ID, Title: "New Spring", SortTitle: "New Spring",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, hidden); err != nil {
		t.Fatal(err)
	}

	s := &models.Series{ForeignID: "ol-series:wheel-of-time", Title: "The Wheel of Time"}
	if err := seriesRepo.CreateOrGet(ctx, s); err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, kept.ID, "1", true); err != nil {
		t.Fatalf("LinkBook kept: %v", err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, hidden.ID, "2", true); err != nil {
		t.Fatalf("LinkBook hidden: %v", err)
	}

	// Exclude the second book after linking it.
	if err := bookRepo.SetExcluded(ctx, hidden.ID, true); err != nil {
		t.Fatalf("SetExcluded: %v", err)
	}

	// ListBooksInSeries hides the excluded book (used by series fill / genre apply).
	visible, err := seriesRepo.ListBooksInSeries(ctx, s.ID)
	if err != nil {
		t.Fatalf("ListBooksInSeries: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != kept.ID {
		t.Fatalf("ListBooksInSeries should hide the excluded book, got %+v", visible)
	}

	// ListBooksInSeriesIncludingExcluded still returns both (used by rollback counting).
	all, err := seriesRepo.ListBooksInSeriesIncludingExcluded(ctx, s.ID)
	if err != nil {
		t.Fatalf("ListBooksInSeriesIncludingExcluded: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListBooksInSeriesIncludingExcluded should return both books, got %d: %+v", len(all), all)
	}
	gotIDs := map[int64]bool{}
	for _, b := range all {
		gotIDs[b.ID] = true
	}
	if !gotIDs[kept.ID] || !gotIDs[hidden.ID] {
		t.Fatalf("ListBooksInSeriesIncludingExcluded should include both the kept and the excluded book, got %+v", all)
	}
}

// TestSeriesDisplayQueriesReturnExcludedFlag covers #2324: the two queries
// behind GET /series and GET /series/{id} must carry books.excluded through to
// models.Book.Excluded, so the series view can tell an excluded book from a
// wanted one instead of counting it as a gap. Follow-up to #2302/#2310, which
// fixed the action path (ListBooksInSeries) but left the display path reporting
// "excluded": false for every book.
func TestSeriesDisplayQueriesReturnExcludedFlag(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	author := &models.Author{
		ForeignID: "OL-2324-A", Name: "Isaac Asimov", SortName: "Asimov, Isaac",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	kept := &models.Book{
		ForeignID: "OL-2324-K", AuthorID: author.ID, Title: "Foundation", SortTitle: "Foundation",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, kept); err != nil {
		t.Fatal(err)
	}
	hidden := &models.Book{
		ForeignID: "OL-2324-H", AuthorID: author.ID, Title: "Second Foundation", SortTitle: "Second Foundation",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, hidden); err != nil {
		t.Fatal(err)
	}

	s := &models.Series{ForeignID: "ol-series:foundation", Title: "Foundation"}
	if err := seriesRepo.CreateOrGet(ctx, s); err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, kept.ID, "1", true); err != nil {
		t.Fatalf("LinkBook kept: %v", err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, hidden.ID, "2", true); err != nil {
		t.Fatalf("LinkBook hidden: %v", err)
	}
	if err := bookRepo.SetExcluded(ctx, hidden.ID, true); err != nil {
		t.Fatalf("SetExcluded: %v", err)
	}

	want := map[int64]bool{kept.ID: false, hidden.ID: true}

	collect := func(books []models.SeriesBook) map[int64]bool {
		got := map[int64]bool{}
		for _, sb := range books {
			if sb.Book != nil {
				got[sb.Book.ID] = sb.Book.Excluded
			}
		}
		return got
	}

	// GET /series path.
	list, err := seriesRepo.ListWithBooksForUser(ctx, 0)
	if err != nil {
		t.Fatalf("ListWithBooksForUser: %v", err)
	}
	var found *models.Series
	for i := range list {
		if list[i].ID == s.ID {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("ListWithBooksForUser did not return series %d", s.ID)
	}
	if got := collect(found.Books); got[kept.ID] != want[kept.ID] || got[hidden.ID] != want[hidden.ID] {
		t.Errorf("ListWithBooksForUser excluded flags = %+v, want %+v", got, want)
	}

	// GET /series/{id} path.
	one, err := seriesRepo.GetByIDForUser(ctx, s.ID, 0)
	if err != nil {
		t.Fatalf("GetByIDForUser: %v", err)
	}
	if one == nil {
		t.Fatalf("GetByIDForUser returned nil for series %d", s.ID)
	}
	if got := collect(one.Books); got[kept.ID] != want[kept.ID] || got[hidden.ID] != want[hidden.ID] {
		t.Errorf("GetByIDForUser excluded flags = %+v, want %+v", got, want)
	}
}

func TestSeriesManualManagement(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	author := &models.Author{
		ForeignID: "OL1A", Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL1W", AuthorID: author.ID, Title: "Dune", SortTitle: "Dune",
		Status: models.BookStatusImported, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	series, err := seriesRepo.CreateManual(ctx, "  Dune Chronicles  ")
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if series.ID == 0 || series.Title != "Dune Chronicles" {
		t.Fatalf("unexpected manual series: %+v", series)
	}
	if !strings.HasPrefix(series.ForeignID, "manual:series:") {
		t.Fatalf("foreign id prefix: got %q, want manual:series:", series.ForeignID)
	}

	if err := seriesRepo.UpdateTitle(ctx, series.ID, "Dune Saga"); err != nil {
		t.Fatalf("UpdateTitle: %v", err)
	}
	got, err := seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if got.Title != "Dune Saga" {
		t.Fatalf("title = %q, want Dune Saga", got.Title)
	}

	if err := seriesRepo.UpsertBookLink(ctx, series.ID, book.ID, "1", true); err != nil {
		t.Fatalf("first UpsertBookLink: %v", err)
	}
	if err := seriesRepo.UpsertBookLink(ctx, series.ID, book.ID, "1.5", false); err != nil {
		t.Fatalf("second UpsertBookLink: %v", err)
	}
	got, err = seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatalf("GetByID after link: %v", err)
	}
	if len(got.Books) != 1 {
		t.Fatalf("expected one linked book, got %+v", got.Books)
	}
	if got.Books[0].PositionInSeries != "1.5" || got.Books[0].PrimarySeries {
		t.Fatalf("expected updated link metadata, got %+v", got.Books[0])
	}

	if err := seriesRepo.Delete(ctx, series.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := bookRepo.GetByID(ctx, book.ID); err != nil || got == nil {
		t.Fatalf("delete series should preserve book, got book=%+v err=%v", got, err)
	}
}

func TestSeriesList(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewSeriesRepo(database)

	// Empty list.
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0, got %d", len(list))
	}

	// Add two series.
	for _, title := range []string{"Alpha Series", "Beta Series"} {
		s := &models.Series{ForeignID: "ol-series:" + title, Title: title}
		if err := repo.CreateOrGet(ctx, s); err != nil {
			t.Fatalf("CreateOrGet %q: %v", title, err)
		}
	}

	list, err = repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 series, got %d", len(list))
	}
}

func TestSeriesHardcoverLinkCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewSeriesRepo(database)
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "Stormlight Archive"}
	if err := repo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   "hc-series:1",
		HardcoverProviderID: "1",
		HardcoverTitle:      "The Stormlight Archive",
		HardcoverAuthorName: "Brandon Sanderson",
		HardcoverBookCount:  10,
		Confidence:          0.82,
		LinkedBy:            "auto",
	}
	if err := repo.UpsertHardcoverLink(ctx, link); err != nil {
		t.Fatalf("upsert link: %v", err)
	}
	if link.ID == 0 {
		t.Fatal("expected stored link id")
	}

	got, err := repo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	if got == nil || got.HardcoverTitle != "The Stormlight Archive" || got.LinkedBy != "auto" {
		t.Fatalf("unexpected link: %+v", got)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list series: %v", err)
	}
	if list[0].HardcoverLink == nil || list[0].HardcoverLink.HardcoverSeriesID != "hc-series:1" {
		t.Fatalf("expected hydrated link in list, got %+v", list[0].HardcoverLink)
	}

	link.HardcoverTitle = "Stormlight Archive"
	link.LinkedBy = "manual"
	link.Confidence = 1
	if err := repo.UpsertHardcoverLink(ctx, link); err != nil {
		t.Fatalf("update link: %v", err)
	}
	got, err = repo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatalf("get updated link: %v", err)
	}
	if got.HardcoverTitle != "Stormlight Archive" || got.LinkedBy != "manual" || got.Confidence != 1 {
		t.Fatalf("unexpected updated link: %+v", got)
	}

	if err := repo.DeleteHardcoverLink(ctx, series.ID); err != nil {
		t.Fatalf("delete link: %v", err)
	}
	got, err = repo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatalf("get deleted link: %v", err)
	}
	if got != nil {
		t.Fatalf("expected deleted link, got %+v", got)
	}
}

func TestSeriesListWithBooksHydratesBooksAndHardcoverLinks(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	empty := &models.Series{ForeignID: "manual:empty", Title: "Empty Series"}
	if err := seriesRepo.Create(ctx, empty); err != nil {
		t.Fatal(err)
	}
	linked := &models.Series{ForeignID: "manual:stormlight", Title: "Stormlight Archive"}
	if err := seriesRepo.Create(ctx, linked); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "hc:the-way-of-kings", AuthorID: author.ID, Title: "The Way of Kings", SortTitle: "The Way of Kings",
		Status: models.BookStatusWanted, Monitored: true, Genres: []string{},
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, linked.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            linked.ID,
		HardcoverSeriesID:   "hc-series:42",
		HardcoverProviderID: "42",
		HardcoverTitle:      "The Stormlight Archive",
		HardcoverAuthorName: "Brandon Sanderson",
		HardcoverBookCount:  10,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := seriesRepo.ListWithBooks(ctx)
	if err != nil {
		t.Fatalf("ListWithBooks: %v", err)
	}
	byID := make(map[int64]models.Series, len(got))
	for _, series := range got {
		byID[series.ID] = series
	}
	if len(byID) != 2 {
		t.Fatalf("series count = %d, want 2: %+v", len(byID), got)
	}
	if len(byID[empty.ID].Books) != 0 {
		t.Fatalf("empty series books = %+v, want none", byID[empty.ID].Books)
	}
	hydrated := byID[linked.ID]
	if len(hydrated.Books) != 1 || hydrated.Books[0].Book == nil || hydrated.Books[0].Book.Title != "The Way of Kings" {
		t.Fatalf("hydrated books = %+v, want linked book", hydrated.Books)
	}
	if hydrated.Books[0].PositionInSeries != "1" || !hydrated.Books[0].PrimarySeries {
		t.Fatalf("series book metadata = %+v, want position 1 primary", hydrated.Books[0])
	}
	if hydrated.HardcoverLink == nil || hydrated.HardcoverLink.HardcoverSeriesID != "hc-series:42" {
		t.Fatalf("hydrated link = %+v, want hc-series:42", hydrated.HardcoverLink)
	}
}

func TestSeriesMonitoringAndBookLinks(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	series := &models.Series{ForeignID: "manual:murderbot", Title: "Murderbot Diaries"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.SetMonitored(ctx, series.ID, true); err != nil {
		t.Fatalf("SetMonitored true: %v", err)
	}
	gotSeries, err := seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSeries == nil || !gotSeries.Monitored {
		t.Fatalf("monitored series = %+v, want monitored", gotSeries)
	}

	author := &models.Author{ForeignID: "ol:martha-wells", Name: "Martha Wells", SortName: "Wells, Martha"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:all-systems-red", AuthorID: author.ID, Title: "All Systems Red", SortTitle: "All Systems Red",
		Status: models.BookStatusWanted, Monitored: true, Genres: []string{},
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatalf("ListBooksInSeries: %v", err)
	}
	if len(books) != 1 || books[0].ID != book.ID || books[0].Status != models.BookStatusWanted || !books[0].Monitored {
		t.Fatalf("series books = %+v, want linked wanted monitored book", books)
	}

	if err := seriesRepo.UnlinkBook(ctx, series.ID, book.ID); err != nil {
		t.Fatalf("UnlinkBook: %v", err)
	}
	books, err = seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatalf("ListBooksInSeries after unlink: %v", err)
	}
	if len(books) != 0 {
		t.Fatalf("series books after unlink = %+v, want none", books)
	}
}

func TestSeriesForeignIDAndPrimarySeriesLookup(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	if got, err := seriesRepo.GetByForeignID(ctx, "missing"); err != nil || got != nil {
		t.Fatalf("missing GetByForeignID = %+v err=%v, want nil", got, err)
	}
	series := &models.Series{ForeignID: "abs:series:old", Title: "The Expanse"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	got, err := seriesRepo.GetByForeignID(ctx, "abs:series:old")
	if err != nil {
		t.Fatalf("GetByForeignID old: %v", err)
	}
	if got == nil || got.ID != series.ID {
		t.Fatalf("GetByForeignID old = %+v, want series %d", got, series.ID)
	}
	if err := seriesRepo.UpdateForeignID(ctx, series.ID, "hc-series:expanse"); err != nil {
		t.Fatalf("UpdateForeignID: %v", err)
	}
	if got, err := seriesRepo.GetByForeignID(ctx, "abs:series:old"); err != nil || got != nil {
		t.Fatalf("old foreign id after update = %+v err=%v, want nil", got, err)
	}
	got, err = seriesRepo.GetByForeignID(ctx, "hc-series:expanse")
	if err != nil {
		t.Fatalf("GetByForeignID updated: %v", err)
	}
	if got == nil || got.ID != series.ID || got.ForeignID != "hc-series:expanse" {
		t.Fatalf("GetByForeignID updated = %+v, want updated series", got)
	}

	author := &models.Author{ForeignID: "ol:james-corey", Name: "James S. A. Corey", SortName: "Corey, James S. A."}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:leviathan-wakes", AuthorID: author.ID, Title: "Leviathan Wakes", SortTitle: "Leviathan Wakes",
		Status: models.BookStatusWanted, Genres: []string{},
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	title, position, err := seriesRepo.GetPrimarySeriesForBook(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetPrimarySeriesForBook: %v", err)
	}
	if title != "The Expanse" || position != "1" {
		t.Fatalf("primary series = %q/%q, want The Expanse/1", title, position)
	}
	title, position, err = seriesRepo.GetPrimarySeriesForBook(ctx, book.ID+999)
	if err != nil {
		t.Fatalf("GetPrimarySeriesForBook missing: %v", err)
	}
	if title != "" || position != "" {
		t.Fatalf("missing primary series = %q/%q, want empty", title, position)
	}
}

func TestSeriesGetBookBySeriesPosition(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	author := &models.Author{ForeignID: "ol:james-corey", Name: "James S. A. Corey", SortName: "Corey, James S. A."}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	wanted := &models.Book{
		ForeignID: "ol:leviathan-wakes", AuthorID: author.ID, Title: "Leviathan Wakes", SortTitle: "Leviathan Wakes",
		Status: models.BookStatusWanted, Monitored: true, Genres: []string{},
	}
	if err := bookRepo.Create(ctx, wanted); err != nil {
		t.Fatal(err)
	}
	imported := &models.Book{
		ForeignID: "ol:calibans-war", AuthorID: author.ID, Title: "Caliban's War", SortTitle: "Caliban's War",
		Status: models.BookStatusImported, Monitored: true, Genres: []string{},
	}
	if err := bookRepo.Create(ctx, imported); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{ForeignID: "manual:expanse", Title: "The Expanse"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, wanted.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, imported.ID, "2", true); err != nil {
		t.Fatal(err)
	}

	got, err := seriesRepo.GetBookBySeriesPosition(ctx, " the expanse ", "1")
	if err != nil {
		t.Fatalf("GetBookBySeriesPosition wanted: %v", err)
	}
	if got == nil || got.ID != wanted.ID {
		t.Fatalf("wanted match = %+v, want book %d", got, wanted.ID)
	}
	got, err = seriesRepo.GetBookBySeriesPosition(ctx, "The Expanse", "99")
	if err != nil {
		t.Fatalf("GetBookBySeriesPosition missing: %v", err)
	}
	if got != nil {
		t.Fatalf("missing position = %+v, want nil", got)
	}
	got, err = seriesRepo.GetBookBySeriesPosition(ctx, "The Expanse", "2")
	if err != nil {
		t.Fatalf("GetBookBySeriesPosition imported: %v", err)
	}
	if got != nil {
		t.Fatalf("imported position = %+v, want nil because only wanted books qualify", got)
	}

	duplicateSeries := &models.Series{ForeignID: "manual:expanse-duplicate", Title: " the expanse "}
	if err := seriesRepo.Create(ctx, duplicateSeries); err != nil {
		t.Fatal(err)
	}
	duplicateBook := &models.Book{
		ForeignID: "ol:leviathan-wakes-duplicate", AuthorID: author.ID, Title: "Leviathan Wakes", SortTitle: "Leviathan Wakes",
		Status: models.BookStatusWanted, Monitored: true, Genres: []string{},
	}
	if err := bookRepo.Create(ctx, duplicateBook); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, duplicateSeries.ID, duplicateBook.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	got, err = seriesRepo.GetBookBySeriesPosition(ctx, "The Expanse", "1")
	if err != nil {
		t.Fatalf("GetBookBySeriesPosition ambiguous: %v", err)
	}
	if got != nil {
		t.Fatalf("ambiguous position = %+v, want nil", got)
	}
}

func TestSeriesGenreOverridePersistence(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewSeriesRepo(database)
	series, err := repo.CreateManual(ctx, "Demo Series")
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.SetGenreOverride(ctx, series.ID, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.GenreOverrideSet || stored.GenreOverride == nil || len(stored.GenreOverride) != 0 {
		t.Fatalf("empty genre override not preserved: %+v", stored)
	}

	if _, err := database.ExecContext(ctx, "UPDATE series SET genre_override='not-json' WHERE id=?", series.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetByID(ctx, series.ID); err == nil || !strings.Contains(err.Error(), "decode series") {
		t.Fatalf("expected malformed override error, got %v", err)
	}

	if _, err := database.ExecContext(ctx, `
		CREATE TRIGGER reject_series_genre_override
		BEFORE UPDATE OF genre_override ON series
		BEGIN
			SELECT RAISE(FAIL, 'genre override rejected');
		END`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetGenreOverride(ctx, series.ID, []string{"Fantasy"}); err == nil || !strings.Contains(err.Error(), "update series") {
		t.Fatalf("expected update error, got %v", err)
	}
}

func TestSeriesGenreOverrideClearAndListVisibility(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewSeriesRepo(database)
	series, err := repo.CreateManual(ctx, "Demo Series")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetGenreOverride(ctx, series.ID, []string{"Fantasy"}); err != nil {
		t.Fatal(err)
	}

	// The override has to survive the list queries, not just GetByID — those
	// are what the series page reads, so an override invisible there cannot be
	// shown or cleared by the user (#1709 follow-up).
	listed, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].GenreOverrideSet || len(listed[0].GenreOverride) != 1 || listed[0].GenreOverride[0] != "Fantasy" {
		t.Fatalf("List dropped the override: %+v", listed)
	}
	withBooks, err := repo.ListWithBooksForUser(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(withBooks) != 1 || !withBooks[0].GenreOverrideSet || len(withBooks[0].GenreOverride) != 1 {
		t.Fatalf("ListWithBooksForUser dropped the override: %+v", withBooks)
	}

	// Clearing must reach "no override", which is distinct from an override of
	// no genres: the latter locks every future book to an empty genre list.
	if err := repo.SetGenreOverride(ctx, series.ID, []string{}); err != nil {
		t.Fatal(err)
	}
	empty, err := repo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.GenreOverrideSet || len(empty.GenreOverride) != 0 {
		t.Fatalf("empty override should stay set: %+v", empty)
	}
	if err := repo.ClearGenreOverride(ctx, series.ID); err != nil {
		t.Fatal(err)
	}
	cleared, err := repo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.GenreOverrideSet || cleared.GenreOverride != nil {
		t.Fatalf("expected no override after clear, got set=%v value=%#v", cleared.GenreOverrideSet, cleared.GenreOverride)
	}
}

// TestEnsureHardcoverLinkFromForeignID covers the #2245 helper against a real
// SQLite-backed repo: a provider-supplied hc-series foreign id must produce a
// series_hardcover_links row exactly once, and anything else must write
// nothing.
func TestEnsureHardcoverLinkFromForeignID(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewSeriesRepo(database)
	series := &models.Series{ForeignID: "hc-series:1017", Title: "Children of Time"}
	if err := repo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	linked, err := repo.EnsureHardcoverLinkFromForeignID(ctx, series.ID, "hc-series:1017", "  Children of Time  ")
	if err != nil {
		t.Fatalf("ensure link: %v", err)
	}
	if !linked {
		t.Fatal("expected the first call to report a written link")
	}
	got, err := repo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	if got == nil {
		t.Fatal("no link row was written")
	}
	if got.HardcoverSeriesID != "hc-series:1017" {
		t.Errorf("hardcover series id = %q, want hc-series:1017", got.HardcoverSeriesID)
	}
	// The provider id is derived from the foreign id by UpsertHardcoverLink.
	if got.HardcoverProviderID != "1017" {
		t.Errorf("hardcover provider id = %q, want 1017", got.HardcoverProviderID)
	}
	if got.HardcoverTitle != "Children of Time" {
		t.Errorf("hardcover title = %q, want the trimmed title", got.HardcoverTitle)
	}
	if got.Confidence != 1 {
		t.Errorf("confidence = %v, want 1 for a provider-supplied identity", got.Confidence)
	}
	if got.LinkedBy != "auto" {
		t.Errorf("linked_by = %q, want auto", got.LinkedBy)
	}

	// Idempotent: a second call writes nothing and changes nothing.
	linked, err = repo.EnsureHardcoverLinkFromForeignID(ctx, series.ID, "hc-series:1017", "Children of Time")
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if linked {
		t.Error("second call must not report a written link")
	}
	again, err := repo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again == nil || again.ID != got.ID || !again.LinkedAt.Equal(got.LinkedAt) {
		t.Errorf("second call altered the row: first %+v, second %+v", got, again)
	}
}

// TestEnsureHardcoverLinkFromForeignIDIgnoresNonHardcoverIDs pins the guard:
// only a foreign id carrying the hc-series prefix identifies a Hardcover
// series. OpenLibrary ids, Hardcover BOOK ids ("hc:"), empty ids and a zero
// series id must all be a no-op, not an error.
func TestEnsureHardcoverLinkFromForeignIDIgnoresNonHardcoverIDs(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewSeriesRepo(database)
	series := &models.Series{ForeignID: "ol-series:children", Title: "Children of Time"}
	if err := repo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		seriesID  int64
		foreignID string
	}{
		{name: "openlibrary id", seriesID: series.ID, foreignID: "ol-series:children"},
		{name: "hardcover book id", seriesID: series.ID, foreignID: "hc:children-of-time"},
		{name: "empty id", seriesID: series.ID, foreignID: ""},
		{name: "zero series id", seriesID: 0, foreignID: "hc-series:1017"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linked, err := repo.EnsureHardcoverLinkFromForeignID(ctx, tc.seriesID, tc.foreignID, "Children of Time")
			if err != nil {
				t.Fatalf("expected a no-op, got error: %v", err)
			}
			if linked {
				t.Error("expected no link to be reported")
			}
		})
	}
	got, err := repo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("a non-Hardcover id produced a link row: %+v", got)
	}
}

// TestEnsureHardcoverLinkFromForeignIDKeepsExistingLink pins that an existing
// link is never overwritten, whatever wrote it: the user's manual choice of a
// different Hardcover series must survive the automatic path (#2245).
func TestEnsureHardcoverLinkFromForeignIDKeepsExistingLink(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewSeriesRepo(database)
	series := &models.Series{ForeignID: "hc-series:1017", Title: "Children of Time"}
	if err := repo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:          series.ID,
		HardcoverSeriesID: "hc-series:9999",
		HardcoverTitle:    "Chosen By Hand",
		Confidence:        1,
		LinkedBy:          "manual",
	}); err != nil {
		t.Fatal(err)
	}

	linked, err := repo.EnsureHardcoverLinkFromForeignID(ctx, series.ID, "hc-series:1017", "Children of Time")
	if err != nil {
		t.Fatalf("ensure over existing link: %v", err)
	}
	if linked {
		t.Error("an existing link was reported as rewritten")
	}
	got, err := repo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.LinkedBy != "manual" || got.HardcoverSeriesID != "hc-series:9999" || got.HardcoverTitle != "Chosen By Hand" {
		t.Fatalf("manual link was not preserved: %+v", got)
	}
}

// TestEnsureHardcoverLinkFromForeignIDPropagatesWriteErrors drives the upsert
// error branch with a real constraint rather than a broken repo: the link
// table's series_id references series(id) and foreign keys are connection
// state OpenMemory switches on, so a series id that does not exist fails the
// insert and the error must reach the caller.
func TestEnsureHardcoverLinkFromForeignIDPropagatesWriteErrors(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewSeriesRepo(database)

	linked, err := repo.EnsureHardcoverLinkFromForeignID(ctx, 99999, "hc-series:1017", "Children of Time")
	if err == nil {
		t.Fatal("expected a foreign-key error for a nonexistent series id")
	}
	if linked {
		t.Error("a failed write must not report a link")
	}
	row := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM series_hardcover_links")
	var count int
	if err := row.Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed ensure left %d link rows behind", count)
	}
}

// TestEnsureHardcoverLinkFromForeignIDSurfacesReadError ensures a failure
// reading the existing link propagates rather than being swallowed, following
// the closed-handle convention TestBackfillAuthorSortKeys_SurfacesReadError
// uses. A swallowed read error here would fall through to the insert and
// clobber-or-duplicate the very link the read exists to protect.
func TestEnsureHardcoverLinkFromForeignIDSurfacesReadError(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	repo := NewSeriesRepo(database)
	database.Close() // query against a closed handle errors on the read phase

	linked, err := repo.EnsureHardcoverLinkFromForeignID(context.Background(), 1, "hc-series:1017", "Children of Time")
	if err == nil {
		t.Fatal("expected an error against a closed database, got nil")
	}
	if linked {
		t.Error("a failed read must not report a link")
	}
}
