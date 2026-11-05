package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// monitorCatalog is a Stormlight shaped catalogue with one of each row a
// scheduled check has to tell apart: the owned volume 1, a back catalogue
// volume 2, a future dated volume 3, a box set, and a split edition part of
// volume 1.
func monitorCatalog(future time.Time) *metadata.SeriesCatalog {
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "hardcover"}
	past := time.Date(2014, 3, 4, 0, 0, 0, 0, time.UTC)
	row := func(id, title, pos string, date *time.Time) metadata.SeriesCatalogBook {
		return metadata.SeriesCatalogBook{
			ForeignID: id, ProviderID: id, Title: title, Position: pos,
			Book: models.Book{ForeignID: id, Title: title, SortTitle: title, MetadataProvider: "hardcover", Author: author, ReleaseDate: date},
		}
	}
	return &metadata.SeriesCatalog{
		ForeignID: "hc-series:42", ProviderID: "42", Title: "The Stormlight Archive", AuthorName: "Brandon Sanderson", BookCount: 5,
		Books: []metadata.SeriesCatalogBook{
			row("hc:the-way-of-kings", "The Way of Kings", "1", &past),
			row("hc:words-of-radiance", "Words of Radiance", "2", &past),
			row("hc:oathbringer", "Oathbringer", "3", &future),
			row("hc:stormlight-box", "The Stormlight Archive 3 Books Collection Set", "1", &past),
			row("hc:wok-part-1", "The Way of Kings, Part 1", "1.1", &past),
		},
	}
}

type monitorFixture struct {
	h      *SeriesHandler
	series *models.Series
	books  *db.BookRepo
	repo   *db.SeriesRepo
	future time.Time
}

func newMonitorFixture(t *testing.T, mode string) *monitorFixture {
	t.Helper()
	future := time.Now().UTC().Add(60 * 24 * time.Hour).Truncate(time.Second)
	catalog := monitorCatalog(future)
	h, repo, authors, books := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, nil)
	ctx := context.Background()
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "hardcover"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{ForeignID: "hc-series:42", Title: "The Stormlight Archive"}
	if err := repo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID: series.ID, HardcoverSeriesID: catalog.ForeignID, HardcoverProviderID: "42",
		HardcoverTitle: catalog.Title, HardcoverAuthorName: catalog.AuthorName, HardcoverBookCount: 5, Confidence: 1, LinkedBy: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	released := time.Date(2010, 8, 31, 0, 0, 0, 0, time.UTC)
	owned := &models.Book{
		ForeignID: "hc:the-way-of-kings", AuthorID: author.ID, Title: "The Way of Kings", SortTitle: "The Way of Kings",
		Status: models.BookStatusImported, Monitored: true, MediaType: models.MediaTypeEbook, Genres: []string{},
		MetadataProvider: "hardcover", ReleaseDate: &released,
	}
	if err := books.Create(ctx, owned); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkBook(ctx, series.ID, owned.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetMonitoring(ctx, series.ID, true, mode, 0); err != nil {
		t.Fatal(err)
	}
	return &monitorFixture{h: h, series: series, books: books, repo: repo, future: future}
}

func (f *monitorFixture) check(t *testing.T) SeriesCheckResult {
	t.Helper()
	res, err := f.h.CheckSeries(context.Background(), f.series)
	if err != nil {
		t.Fatalf("CheckSeries: %v", err)
	}
	return res
}

func (f *monitorFixture) book(t *testing.T, foreignID string) *models.Book {
	t.Helper()
	b, err := f.books.GetByForeignID(context.Background(), foreignID)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSeriesCheckAddsOnlyTheNewCatalogueEntry is the #2523 core: a monitored
// series gets its one new, future dated catalogue entry added, once; the back
// catalogue volume, the box set and the split edition part are not added; and a
// second check adds nothing.
func TestSeriesCheckAddsOnlyTheNewCatalogueEntry(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorUpcoming)

	first := f.check(t)
	if first.Status != models.SeriesCheckOK || first.Created != 1 || first.Upcoming != 1 {
		t.Fatalf("first check = %+v, want ok, 1 created, 1 upcoming", first)
	}
	oath := f.book(t, "hc:oathbringer")
	if oath == nil || !oath.Monitored || oath.Status != models.BookStatusWanted {
		t.Fatalf("Oathbringer was not added as a monitored wanted book: %+v", oath)
	}
	for _, id := range []string{"hc:words-of-radiance", "hc:stormlight-box", "hc:wok-part-1"} {
		if f.book(t, id) != nil {
			t.Errorf("%s was added, want it left out in upcoming mode", id)
		}
	}
	holds, err := f.books.SearchHolds(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if until, ok := holds[oath.ID]; !ok || !until.Equal(f.future) {
		t.Errorf("Oathbringer hold = %v (%v), want %v", until, ok, f.future)
	}

	second := f.check(t)
	if second.Created != 0 {
		t.Errorf("second check created %d books, want 0 (idempotent)", second.Created)
	}
	all, err := f.books.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("library holds %d books after two checks, want 2", len(all))
	}
}

// TestSeriesCheckAllModeAddsTheBackCatalogue: all mode adds the released volume
// 2 as well, without a hold, and still never the box set or the split part.
func TestSeriesCheckAllModeAddsTheBackCatalogue(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorAll)
	res := f.check(t)
	if res.Created != 2 || res.Upcoming != 1 {
		t.Fatalf("check = %+v, want 2 created, 1 upcoming", res)
	}
	words := f.book(t, "hc:words-of-radiance")
	if words == nil {
		t.Fatal("Words of Radiance was not added in all mode")
	}
	holds, _ := f.books.SearchHolds(context.Background(), time.Now())
	if _, held := holds[words.ID]; held {
		t.Error("a released book was held as Upcoming")
	}
	for _, id := range []string{"hc:stormlight-box", "hc:wok-part-1"} {
		if f.book(t, id) != nil {
			t.Errorf("%s was added, want it left out", id)
		}
	}
}

// TestSeriesCheckSkipsAnUnmonitoredSeries: the flag is what the job reads.
func TestSeriesCheckSkipsAnUnmonitoredSeries(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorUpcoming)
	if err := f.repo.SetMonitoring(context.Background(), f.series.ID, false, models.SeriesMonitorUpcoming, 0); err != nil {
		t.Fatal(err)
	}
	res := f.check(t)
	if res.Status != "" || res.Created != 0 {
		t.Errorf("check of an unmonitored series = %+v, want skipped", res)
	}
	if f.book(t, "hc:oathbringer") != nil {
		t.Error("an unmonitored series had a book added")
	}
}

// TestSeriesCheckFollowsAMovedReleaseDate: metadata keeps being read, so a date
// that moves moves the hold, and a date that arrives lifts it.
func TestSeriesCheckFollowsAMovedReleaseDate(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorUpcoming)
	f.check(t)
	oath := f.book(t, "hc:oathbringer")

	moved := f.future.Add(30 * 24 * time.Hour)
	setCatalogDate(t, f, "hc:oathbringer", &moved)
	f.check(t)
	holds, _ := f.books.SearchHolds(context.Background(), time.Now())
	if until := holds[oath.ID]; !until.Equal(moved) {
		t.Errorf("hold after the date moved = %v, want %v", until, moved)
	}

	past := time.Now().UTC().Add(-24 * time.Hour)
	setCatalogDate(t, f, "hc:oathbringer", &past)
	f.check(t)
	holds, _ = f.books.SearchHolds(context.Background(), time.Now())
	if _, held := holds[oath.ID]; held {
		t.Error("the hold survived the release date arriving")
	}
}

func setCatalogDate(t *testing.T, f *monitorFixture, foreignID string, d *time.Time) {
	t.Helper()
	cat, err := f.h.meta.GetSeriesCatalog(context.Background(), "hc-series:42")
	if err != nil {
		t.Fatal(err)
	}
	for i := range cat.Books {
		if cat.Books[i].ForeignID == foreignID {
			cat.Books[i].Book.ReleaseDate = d
		}
	}
}

// TestSeriesCheckReportsAMissingLink: no Hardcover link is a status the UI
// explains, not a silent no-op.
func TestSeriesCheckReportsAMissingLink(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorUpcoming)
	if err := f.repo.DeleteHardcoverLink(context.Background(), f.series.ID); err != nil {
		t.Fatal(err)
	}
	res := f.check(t)
	if res.Status != models.SeriesCheckNoLink || res.Message == "" {
		t.Errorf("check = %+v, want no_link with a message", res)
	}
}

// TestSeriesCheckRefusesAmbiguousPosition: a catalogue row at a position the
// library already holds is as likely a reprint as a new book, so an unattended
// check leaves it for a person.
func TestSeriesCheckRefusesAmbiguousPosition(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorAll)
	cat, _ := f.h.meta.GetSeriesCatalog(context.Background(), "hc-series:42")
	d := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	cat.Books = append(cat.Books, metadata.SeriesCatalogBook{
		ForeignID: "hc:wok-anniversary", ProviderID: "wok-anniv", Title: "Wayfarers and Kings", Position: "1",
		Book: models.Book{ForeignID: "hc:wok-anniversary", Title: "Wayfarers and Kings", MetadataProvider: "hardcover", ReleaseDate: &d,
			Author: &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson"}},
	})
	f.check(t)
	if f.book(t, "hc:wok-anniversary") != nil {
		t.Error("a row sharing volume 1's position was added")
	}
}

// TestSeriesCheckProviderErrorIsReported: a provider failure is returned as the
// provider's error so the job can back off, and is recorded as failed.
func TestSeriesCheckProviderErrorIsReported(t *testing.T) {
	f := newMonitorFixture(t, models.SeriesMonitorUpcoming)
	h, _, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{catalogErr: errors.New("boom")}, nil)
	f.h.meta = h.meta
	res, err := f.h.CheckSeries(context.Background(), f.series)
	if !errors.Is(err, errSeriesMetadataProvider) || res.Status != models.SeriesCheckFailed {
		t.Errorf("res = %+v, err = %v, want failed and a provider error", res, err)
	}
}

func TestSeriesCheckQualifies(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	last := now.Add(-10 * 24 * time.Hour)
	day := func(d int) *time.Time { t := now.Add(time.Duration(d) * 24 * time.Hour); return &t }
	cases := []struct {
		name string
		mode string
		date *time.Time
		last *time.Time
		want bool
	}{
		{"all adds anything", models.SeriesMonitorAll, nil, nil, true},
		{"upcoming needs a date", models.SeriesMonitorUpcoming, nil, &last, false},
		{"upcoming adds a future date", models.SeriesMonitorUpcoming, day(30), nil, true},
		{"upcoming skips the back catalogue", models.SeriesMonitorUpcoming, day(-400), &last, false},
		{"upcoming adds a release since the last check", models.SeriesMonitorUpcoming, day(-3), &last, true},
		{"upcoming skips a past date on a first check", models.SeriesMonitorUpcoming, day(-3), nil, false},
	}
	for _, tc := range cases {
		if got := seriesCheckQualifies(tc.mode, tc.date, now, tc.last); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
