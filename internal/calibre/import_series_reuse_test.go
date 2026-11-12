package calibre

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// seriesReuseFixture is a full production wiring (run tracking plus series)
// with direct repo access, so a test can seed the series an Audiobookshelf
// import or an author sync would have left behind before the Calibre import
// runs (#2173).
type seriesReuseFixture struct {
	imp     *Importer
	fr      *fakeReader
	authors *db.AuthorRepo
	books   *db.BookRepo
	series  *db.SeriesRepo
	runs    *db.CalibreImportRunRepo

	authorIDs map[string]int64
	n         int
}

func newSeriesReuseFixture(t *testing.T) *seriesReuseFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	f := &seriesReuseFixture{
		fr:        &fakeReader{},
		authors:   db.NewAuthorRepo(database),
		books:     db.NewBookRepo(database),
		series:    db.NewSeriesRepo(database),
		runs:      db.NewCalibreImportRunRepo(database),
		authorIDs: map[string]int64{},
	}
	f.imp = NewImporter(f.authors, db.NewAuthorAliasRepo(database), f.books, db.NewEditionRepo(database), db.NewSettingsRepo(database)).
		WithRunTracking(f.runs, db.NewCalibreEntitySnapshotRepo(database), db.NewCalibreProvenanceRepo(database)).
		WithSeries(f.series)
	f.imp.openReader = func(string) (readerIface, error) { return f.fr, nil }
	return f
}

func (f *seriesReuseFixture) author(t *testing.T, name string) int64 {
	t.Helper()
	if id, ok := f.authorIDs[name]; ok {
		return id
	}
	f.n++
	a := &models.Author{ForeignID: fmt.Sprintf("OL%dA", f.n), Name: name, SortName: name, MetadataProvider: "openlibrary"}
	if err := f.authors.Create(context.Background(), a); err != nil {
		t.Fatalf("seed author %q: %v", name, err)
	}
	f.authorIDs[name] = a.ID
	return a.ID
}

// seedSeries creates a series the way another importer would, holding one
// book per entry of books ("Title|Author").
func (f *seriesReuseFixture) seedSeries(t *testing.T, s seededSeries) int64 {
	t.Helper()
	ctx := context.Background()
	row := &models.Series{ForeignID: s.foreignID, Title: s.title}
	if err := f.series.CreateOrGet(ctx, row); err != nil {
		t.Fatalf("seed series %q: %v", s.title, err)
	}
	for pos, b := range s.books {
		f.n++
		book := &models.Book{
			ForeignID: fmt.Sprintf("abs:book:%d", f.n), AuthorID: f.author(t, b.author), Title: b.title, SortTitle: b.title,
			Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MetadataProvider: "audiobookshelf",
		}
		if err := f.books.Create(ctx, book); err != nil {
			t.Fatalf("seed book %q: %v", b.title, err)
		}
		if err := f.series.LinkBook(ctx, row.ID, book.ID, fmt.Sprint(pos+1), true); err != nil {
			t.Fatalf("seed link %q: %v", b.title, err)
		}
	}
	return row.ID
}

// linkHardcover gives a seeded series a Hardcover link naming author, the way
// a metadata sync leaves a linked series that holds no books yet.
func (f *seriesReuseFixture) linkHardcover(t *testing.T, seriesID int64, foreignID, title, author string) {
	t.Helper()
	if err := f.series.UpsertHardcoverLink(context.Background(), &models.SeriesHardcoverLink{
		SeriesID: seriesID, HardcoverSeriesID: foreignID, HardcoverTitle: title, HardcoverAuthorName: author,
	}); err != nil {
		t.Fatalf("link series %d to Hardcover: %v", seriesID, err)
	}
}

type seededBook struct{ title, author string }

type seededSeries struct {
	foreignID string
	title     string
	books     []seededBook
}

// seriesOfCalibreBook returns the ids of the series the imported Calibre book
// is in.
func (f *seriesReuseFixture) seriesOfCalibreBook(t *testing.T, calibreID int64) []int64 {
	t.Helper()
	ctx := context.Background()
	book, err := f.books.GetByCalibreID(ctx, calibreID)
	if err != nil || book == nil {
		t.Fatalf("calibre book %d not found: %v", calibreID, err)
	}
	ids, err := f.series.GetSeriesIDsForBook(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetSeriesIDsForBook: %v", err)
	}
	return ids
}

func (f *seriesReuseFixture) seriesCount(t *testing.T) int {
	t.Helper()
	all, err := f.series.List(context.Background())
	if err != nil {
		t.Fatalf("List series: %v", err)
	}
	return len(all)
}

// TestImporter_SeriesReuse covers how a Calibre series is matched to a series
// that already exists from another importer or a metadata sync (#2173). On
// main every case that expects a seeded series got a new
// "calibre:series:<name>" row instead.
func TestImporter_SeriesReuse(t *testing.T) {
	const calibreRow = -1
	// noAlias means the Calibre series id resolves to no series after the
	// run: the book went into an empty series, which earns no alias (#2173).
	const noAlias = -2
	cases := []struct {
		name string
		seed []seededSeries
		// links gives a seeded series (by index) a Hardcover link naming
		// this author.
		links    map[int]string
		incoming []CalibreBook
		// wantIn is the index into seed of the series each imported book
		// ends up in, or calibreRow for the Calibre import's own row.
		wantIn      []int
		wantSeries  int
		wantReused  int
		wantAliasOn int // index into seed the Calibre id resolves to, calibreRow, or noAlias
	}{
		{
			name: "reuses the Audiobookshelf series of the same title and author",
			seed: []seededSeries{{"abs:series:lib1:aa", "Arcane Ascension", []seededBook{{"On the Shoulders of Titans", "Andrew Rowe"}}}},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Sufficiently Advanced Magic", "Andrew Rowe", "Arcane Ascension", 1),
				seriesCalibreBook(2, "The Torch That Ignites the Stars", "Andrew Rowe", "Arcane Ascension", 3),
			},
			wantIn:      []int{0, 0},
			wantSeries:  1,
			wantReused:  1,
			wantAliasOn: 0,
		},
		{
			name: "matches through the shared title key, filing form included",
			seed: []seededSeries{{"hc-series:123", "The Expanse", []seededBook{{"Leviathan Wakes", "James S. A. Corey"}}}},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Caliban's War", "James S. A. Corey", "Expanse, The", 2),
			},
			wantIn:      []int{0},
			wantSeries:  1,
			wantReused:  1,
			wantAliasOn: 0,
		},
		{
			name: "leaves a same titled series holding only another author's books alone",
			seed: []seededSeries{{"abs:series:lib1:f", "Foundation", []seededBook{{"Foundation and Empire", "Isaac Asimov"}}}},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Foundation Rising", "Someone Else", "Foundation", 1),
			},
			wantIn:      []int{calibreRow},
			wantSeries:  2,
			wantReused:  0,
			wantAliasOn: calibreRow,
		},
		{
			name: "reuses an empty series of the same title",
			seed: []seededSeries{{"ol:series:mistborn", "Mistborn", nil}},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "The Final Empire", "Brandon Sanderson", "Mistborn", 1),
			},
			wantIn:      []int{0},
			wantSeries:  1,
			wantReused:  1,
			wantAliasOn: noAlias,
		},
		{
			name: "skips another author's series and reuses the empty one",
			seed: []seededSeries{
				{"abs:series:lib1:f", "Foundation", []seededBook{{"Foundation and Empire", "Isaac Asimov"}}},
				{"ol:series:foundation", "Foundation", nil},
			},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Foundation Rising", "Someone Else", "Foundation", 1),
			},
			wantIn:      []int{1},
			wantSeries:  2,
			wantReused:  1,
			wantAliasOn: noAlias,
		},
		{
			name: "two acceptable copies, the one already holding the book wins",
			seed: []seededSeries{
				{"abs:series:lib1:c", "Cradle", []seededBook{{"Unsouled", "Will Wight"}}},
				{"abs:series:lib2:c", "Cradle", []seededBook{{"Soulsmith", "Will Wight"}}},
			},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Soulsmith", "Will Wight", "Cradle", 2),
			},
			wantIn:      []int{1},
			wantSeries:  2,
			wantReused:  1,
			wantAliasOn: 1,
		},
		{
			name: "two acceptable copies and nothing decides, falls back to the Calibre row",
			seed: []seededSeries{
				{"abs:series:lib1:c", "Cradle", []seededBook{{"Unsouled", "Will Wight"}}},
				{"abs:series:lib2:c", "Cradle", []seededBook{{"Soulsmith", "Will Wight"}}},
			},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Blackflame", "Will Wight", "Cradle", 3),
				seriesCalibreBook(2, "Skysworn", "Will Wight", "Cradle", 4),
			},
			wantIn:      []int{calibreRow, calibreRow},
			wantSeries:  3,
			wantReused:  0,
			wantAliasOn: calibreRow,
		},
		{
			name: "a sub series with another qualifier is a different series",
			seed: []seededSeries{{"abs:series:lib1:dw", "Discworld (City Watch)", []seededBook{{"Guards! Guards!", "Terry Pratchett"}}}},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Equal Rites", "Terry Pratchett", "Discworld (Witches)", 3),
			},
			wantIn:      []int{calibreRow},
			wantSeries:  2,
			wantReused:  0,
			wantAliasOn: calibreRow,
		},
		{
			name: "a bracketed qualifier is not a parenthesised other one",
			seed: []seededSeries{{"abs:series:lib1:sw", "Star Wars [Legends]", []seededBook{{"Heir to the Empire", "Timothy Zahn"}}}},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Thrawn", "Timothy Zahn", "Star Wars (Canon)", 1),
			},
			wantIn:      []int{calibreRow},
			wantSeries:  2,
			wantReused:  0,
			wantAliasOn: calibreRow,
		},
		{
			name: "a plain twin differing only by its leading article is reused",
			seed: []seededSeries{{"abs:series:lib1:d", "The Legend of Drizzt", []seededBook{{"Homeland", "R. A. Salvatore"}}}},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Exile", "R. A. Salvatore", "Legend of Drizzt", 2),
			},
			wantIn:      []int{0},
			wantSeries:  1,
			wantReused:  1,
			wantAliasOn: 0,
		},
		{
			name: "a series holding the author's books beats an empty linked one",
			seed: []seededSeries{
				{"abs:series:lib1:m", "Mistborn", []seededBook{{"The Well of Ascension", "Brandon Sanderson"}}},
				{"hc-series:100", "Mistborn", nil},
			},
			links: map[int]string{1: "Brandon Sanderson"},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "The Final Empire", "Brandon Sanderson", "Mistborn", 1),
			},
			wantIn:      []int{0},
			wantSeries:  2,
			wantReused:  1,
			wantAliasOn: 0,
		},
		{
			name:  "an empty series linked to another author on Hardcover is not reused",
			seed:  []seededSeries{{"hc-series:200", "Foundation", nil}},
			links: map[int]string{0: "Isaac Asimov"},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "Foundation Rising", "Someone Else", "Foundation", 1),
			},
			wantIn:      []int{calibreRow},
			wantSeries:  2,
			wantReused:  0,
			wantAliasOn: calibreRow,
		},
		{
			name:  "an empty series linked to the same author on Hardcover is reused",
			seed:  []seededSeries{{"hc-series:300", "Mistborn", nil}},
			links: map[int]string{0: "Sanderson, Brandon"},
			incoming: []CalibreBook{
				seriesCalibreBook(1, "The Final Empire", "Brandon Sanderson", "Mistborn", 1),
				seriesCalibreBook(2, "The Well of Ascension", "Brandon Sanderson", "Mistborn", 2),
			},
			wantIn:      []int{0, 0},
			wantSeries:  1,
			wantReused:  2,
			wantAliasOn: noAlias,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeriesReuseFixture(t)
			ctx := context.Background()
			seeded := make([]int64, len(tc.seed))
			for n, s := range tc.seed {
				seeded[n] = f.seedSeries(t, s)
			}
			for n, author := range tc.links {
				f.linkHardcover(t, seeded[n], tc.seed[n].foreignID, tc.seed[n].title, author)
			}
			f.fr.books = tc.incoming

			stats, err := f.imp.Run(ctx, "/lib")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if stats.SeriesFailures != 0 || stats.SeriesLinked != len(tc.incoming) {
				t.Fatalf("series stats linked=%d failures=%d, want %d/0", stats.SeriesLinked, stats.SeriesFailures, len(tc.incoming))
			}
			if stats.SeriesReused != tc.wantReused {
				t.Errorf("SeriesReused = %d, want %d", stats.SeriesReused, tc.wantReused)
			}

			calibreSeries, err := f.series.GetByForeignID(ctx, calibreSeriesForeignID(tc.incoming[0].Series.Name))
			if err != nil {
				t.Fatalf("GetByForeignID: %v", err)
			}
			switch {
			case tc.wantAliasOn == noAlias:
				if calibreSeries != nil {
					t.Errorf("calibre series id resolves to %d (%q), want nothing", calibreSeries.ID, calibreSeries.ForeignID)
				}
			case calibreSeries == nil:
				t.Fatal("the calibre series id resolves to no series")
			case tc.wantAliasOn == calibreRow:
				if calibreSeries.ForeignID != calibreSeriesForeignID(tc.incoming[0].Series.Name) {
					t.Errorf("calibre series id resolves to %d (%q), want the calibre row", calibreSeries.ID, calibreSeries.ForeignID)
				}
			case calibreSeries.ID != seeded[tc.wantAliasOn]:
				t.Errorf("calibre series id resolves to %d, want seeded series %d", calibreSeries.ID, seeded[tc.wantAliasOn])
			}
			resolve := func(idx int) int64 {
				if idx == calibreRow {
					if calibreSeries == nil {
						t.Fatal("expected a calibre row")
					}
					return calibreSeries.ID
				}
				return seeded[idx]
			}

			for n, cb := range tc.incoming {
				got := f.seriesOfCalibreBook(t, cb.CalibreID)
				want := resolve(tc.wantIn[n])
				if len(got) != 1 || got[0] != want {
					t.Errorf("%q is in series %v, want [%d]", cb.Title, got, want)
				}
			}
			if got := f.seriesCount(t); got != tc.wantSeries {
				t.Errorf("series rows = %d, want %d", got, tc.wantSeries)
			}
		})
	}
}

// TestFindReusableSeriesOutsideARun calls the matcher with no run state, the
// way a caller other than run() would: the index loads on demand, and an
// undecided tie still returns no series every time it is asked.
func TestFindReusableSeriesOutsideARun(t *testing.T) {
	f := newSeriesReuseFixture(t)
	ctx := context.Background()
	empty := f.seedSeries(t, seededSeries{"ol:series:mistborn", "Mistborn", nil})
	f.seedSeries(t, seededSeries{"abs:series:lib1:c", "Cradle", []seededBook{{"Unsouled", "Will Wight"}}})
	f.seedSeries(t, seededSeries{"abs:series:lib2:c", "Cradle", []seededBook{{"Soulsmith", "Will Wight"}}})
	book := &models.Book{ID: 9999, AuthorID: f.author(t, "Will Wight")}

	cases := []struct {
		name   string
		series string
		book   *models.Book
		want   int64
	}{
		{"empty series is reused", "Mistborn", book, empty},
		{"undecided tie", "Cradle", book, 0},
		{"undecided tie asked again", "Cradle", book, 0},
		{"no candidate", "Stormlight", book, 0},
		{"title with no key", "!!!", book, 0},
		{"no book", "Mistborn", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := f.imp.findReusableSeries(ctx, tc.book, tc.series)
			if err != nil {
				t.Fatalf("findReusableSeries: %v", err)
			}
			var gotID int64
			if got != nil {
				gotID = got.ID
			}
			if gotID != tc.want {
				t.Errorf("findReusableSeries(%q) = series %d, want %d", tc.series, gotID, tc.want)
			}
		})
	}

	var idx seriesTitleIndex
	idx.add(models.Series{ID: 1, Title: "Mistborn"})
	if idx.byKey != nil {
		t.Errorf("add before load indexed a row: %+v", idx.byKey)
	}
}

// TestFindReusableSeriesSkipsAStaleCandidate checks that a series the run's
// index still lists but that is gone, deleted or merged away mid run, is not
// mistaken for an empty series and reused.
func TestFindReusableSeriesSkipsAStaleCandidate(t *testing.T) {
	f := newSeriesReuseFixture(t)
	ctx := context.Background()
	gone := f.seedSeries(t, seededSeries{"ol:series:mistborn", "Mistborn", nil})
	f.imp.seriesIdx = &seriesTitleIndex{}
	if err := f.imp.seriesIdx.load(ctx, f.series); err != nil {
		t.Fatalf("load index: %v", err)
	}
	if err := f.series.Delete(ctx, gone); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	book := &models.Book{ID: 9999, AuthorID: f.author(t, "Brandon Sanderson")}
	got, _, err := f.imp.findReusableSeries(ctx, book, "Mistborn")
	if err != nil {
		t.Fatalf("findReusableSeries: %v", err)
	}
	if got != nil {
		t.Errorf("findReusableSeries returned deleted series %d, want none", got.ID)
	}
}

// TestImporter_EmptySeriesReuseWritesNoAlias checks that a series reused
// because it was empty gets no alias in that run, not even from the run's own
// second book, so the next run decides again instead of skipping the author
// check for good. Once a later run finds the author's books there, the alias
// is written.
func TestImporter_EmptySeriesReuseWritesNoAlias(t *testing.T) {
	f := newSeriesReuseFixture(t)
	ctx := context.Background()
	empty := f.seedSeries(t, seededSeries{"ol:series:mistborn", "Mistborn", nil})
	f.fr.books = []CalibreBook{
		seriesCalibreBook(1, "The Final Empire", "Brandon Sanderson", "Mistborn", 1),
		seriesCalibreBook(2, "The Well of Ascension", "Brandon Sanderson", "Mistborn", 2),
	}
	if _, err := f.imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	aliases, err := f.series.AliasForeignIDs(ctx, []int64{empty})
	if err != nil {
		t.Fatalf("AliasForeignIDs: %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("aliases after reusing an empty series = %v, want none", aliases)
	}

	stats, err := f.imp.Run(ctx, "/lib")
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	// The first book matches on the books the first run filed and writes the
	// alias; the second then resolves through it.
	if stats.SeriesReused != 1 || f.seriesCount(t) != 1 {
		t.Errorf("second run reused=%d rows=%d, want 1/1", stats.SeriesReused, f.seriesCount(t))
	}
	aliases, err = f.series.AliasForeignIDs(ctx, []int64{empty})
	if err != nil {
		t.Fatalf("AliasForeignIDs: %v", err)
	}
	if len(aliases) != 1 || aliases[0] != calibreSeriesForeignID("Mistborn") {
		t.Errorf("aliases after the second run = %v, want the calibre series id", aliases)
	}
}

// TestImporter_MultiAuthorSeriesIntoEmptySeries checks that a Calibre series
// written by several authors stays whole when it is filed into an empty
// series. The empty reuse writes no alias, so before the run remembered the
// name, the second author's book found a series holding only the first
// author's book and got a Calibre row of its own; the next run then filed the
// first book into that row as well.
func TestImporter_MultiAuthorSeriesIntoEmptySeries(t *testing.T) {
	f := newSeriesReuseFixture(t)
	ctx := context.Background()
	wildCards := f.seedSeries(t, seededSeries{"hc-series:wc", "Wild Cards", nil})
	f.fr.books = []CalibreBook{
		seriesCalibreBook(1, "Wild Cards", "George R. R. Martin", "Wild Cards", 1),
		seriesCalibreBook(2, "Aces High", "Melinda Snodgrass", "Wild Cards", 2),
	}
	check := func(run string) {
		t.Helper()
		if got := f.seriesCount(t); got != 1 {
			t.Errorf("%s: series rows = %d, want 1", run, got)
		}
		for _, id := range []int64{1, 2} {
			if got := f.seriesOfCalibreBook(t, id); len(got) != 1 || got[0] != wildCards {
				t.Errorf("%s: calibre book %d is in series %v, want [%d]", run, id, got, wildCards)
			}
		}
	}

	stats, err := f.imp.Run(ctx, "/lib")
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if stats.SeriesReused != 2 || stats.SeriesFailures != 0 {
		t.Errorf("first run reused=%d failures=%d, want 2/0", stats.SeriesReused, stats.SeriesFailures)
	}
	check("first run")
	if aliases, _ := f.series.AliasForeignIDs(ctx, []int64{wildCards}); len(aliases) != 0 {
		t.Errorf("first run aliases = %v, want none (the series was empty)", aliases)
	}

	if _, err := f.imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	check("second run")
	if aliases, _ := f.series.AliasForeignIDs(ctx, []int64{wildCards}); len(aliases) != 1 {
		t.Errorf("second run aliases = %v, want the calibre series id", aliases)
	}
}

// TestResolveSeriesForgetsADeletedReuse checks that a series the run reused
// and that was deleted since is not handed back for the name again.
func TestResolveSeriesForgetsADeletedReuse(t *testing.T) {
	f := newSeriesReuseFixture(t)
	ctx := context.Background()
	gone := f.seedSeries(t, seededSeries{"ol:series:wc", "Wild Cards", nil})
	if err := f.series.Delete(ctx, gone); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	f.imp.seriesReusedByName = map[string]models.Series{
		calibreSeriesForeignID("Wild Cards"): {ID: gone, ForeignID: "ol:series:wc", Title: "Wild Cards"},
	}
	book := &models.Book{ID: 9999, AuthorID: f.author(t, "Melinda Snodgrass")}
	got, reused, err := f.imp.resolveSeries(ctx, book, "Wild Cards")
	if err != nil {
		t.Fatalf("resolveSeries: %v", err)
	}
	if got == nil || got.ID == gone || reused {
		t.Errorf("resolveSeries = %+v reused=%v, want a new calibre row", got, reused)
	}
	if _, still := f.imp.seriesReusedByName[calibreSeriesForeignID("Wild Cards")]; still {
		t.Error("the deleted series is still remembered for the name")
	}
}

// TestImporter_SeriesResolveFailureCountsAndContinues checks that a database
// error while finding the series is counted on the series side and does not
// panic or abort.
func TestImporter_SeriesResolveFailureCountsAndContinues(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	series := db.NewSeriesRepo(database)
	imp := (&Importer{}).WithSeries(series)
	database.Close()

	stats := &ImportStats{}
	imp.attachBookToSeries(context.Background(), 0, &models.Book{ID: 1, AuthorID: 1}, &CalibreSeries{Name: "Cradle"}, stats)
	if stats.SeriesFailures != 1 || stats.SeriesLinked != 0 {
		t.Errorf("stats failures=%d linked=%d, want 1/0", stats.SeriesFailures, stats.SeriesLinked)
	}
	if _, _, err := imp.findReusableSeries(context.Background(), &models.Book{ID: 1, AuthorID: 1}, "Cradle"); err == nil {
		t.Error("findReusableSeries on a closed database returned no error")
	}
}

// TestImporter_SeriesReuseRerunFindsAlias checks that a rerun after a reuse
// lands on the reused series through the alias and creates nothing.
func TestImporter_SeriesReuseRerunFindsAlias(t *testing.T) {
	f := newSeriesReuseFixture(t)
	ctx := context.Background()
	absID := f.seedSeries(t, seededSeries{"abs:series:lib1:aa", "Arcane Ascension", []seededBook{{"On the Shoulders of Titans", "Andrew Rowe"}}})
	f.fr.books = []CalibreBook{seriesCalibreBook(1, "Sufficiently Advanced Magic", "Andrew Rowe", "Arcane Ascension", 1)}

	if _, err := f.imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	aliases, err := f.series.AliasForeignIDs(ctx, []int64{absID})
	if err != nil {
		t.Fatalf("AliasForeignIDs: %v", err)
	}
	if len(aliases) != 1 || aliases[0] != calibreSeriesForeignID("Arcane Ascension") {
		t.Fatalf("aliases on the reused series = %v, want the calibre series id", aliases)
	}

	stats, err := f.imp.Run(ctx, "/lib")
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if stats.SeriesLinked != 1 || stats.SeriesReused != 0 || stats.SeriesFailures != 0 {
		t.Errorf("rerun stats linked=%d reused=%d failures=%d, want 1/0/0 (found through the alias)",
			stats.SeriesLinked, stats.SeriesReused, stats.SeriesFailures)
	}
	if got := f.seriesCount(t); got != 1 {
		t.Errorf("series rows after rerun = %d, want 1", got)
	}
	if got := f.seriesOfCalibreBook(t, 1); len(got) != 1 || got[0] != absID {
		t.Errorf("book is in series %v after rerun, want [%d]", got, absID)
	}
}

// TestRollback_KeepsReusedSeries is the provenance half of #2173: rolling back
// the Calibre run that filed a book into another importer's series takes the
// book out and leaves the series, and its own books, alone.
func TestRollback_KeepsReusedSeries(t *testing.T) {
	cases := []struct {
		name string
		seed seededSeries
	}{
		{"series holding the author's books", seededSeries{"abs:series:lib1:aa", "Arcane Ascension", []seededBook{{"On the Shoulders of Titans", "Andrew Rowe"}}}},
		{"empty series", seededSeries{"ol:series:aa", "Arcane Ascension", nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeriesReuseFixture(t)
			ctx := context.Background()
			seriesID := f.seedSeries(t, tc.seed)
			f.fr.books = []CalibreBook{seriesCalibreBook(1, "Sufficiently Advanced Magic", "Andrew Rowe", "Arcane Ascension", 1)}

			stats, err := f.imp.Run(ctx, "/lib")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if stats.SeriesReused != 1 {
				t.Fatalf("SeriesReused = %d, want 1", stats.SeriesReused)
			}
			if _, err := f.imp.Rollback(ctx, latestRunID(t, f.runs)); err != nil {
				t.Fatalf("Rollback: %v", err)
			}

			kept, err := f.series.GetByID(ctx, seriesID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if kept == nil {
				t.Fatal("rollback deleted the series the Calibre run had reused")
			}
			if kept.ForeignID != tc.seed.foreignID {
				t.Errorf("reused series foreign id = %q, want %q", kept.ForeignID, tc.seed.foreignID)
			}
			linked, err := f.series.ListBooksInSeries(ctx, seriesID)
			if err != nil {
				t.Fatalf("ListBooksInSeries: %v", err)
			}
			if len(linked) != len(tc.seed.books) {
				t.Fatalf("series holds %d books after rollback, want the %d it had before", len(linked), len(tc.seed.books))
			}
			for _, b := range linked {
				if b.CalibreID != nil {
					t.Errorf("rolled back Calibre book %q is still in the series", b.Title)
				}
			}
		})
	}
}
