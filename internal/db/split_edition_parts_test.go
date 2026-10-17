package db

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestListCoveredSplitEditionParts pins which wholes count as "already in the
// library" for #3048, and that one user's whole never covers another user's
// part.
func TestListCoveredSplitEditionParts(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	series := NewSeriesRepo(database)
	users := NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		title, position, status string
		monitored, excluded     bool
		owner                   int64
	}
	seed := func(seriesTitle string, rows ...row) (int64, []int64) {
		t.Helper()
		// One author per case: a whole by the same author in another series
		// would cover the part across series (#3324) and blur the cases.
		author := &models.Author{ForeignID: "hc:" + seriesTitle, Name: seriesTitle, SortName: seriesTitle, MetadataProvider: "hardcover"}
		if err := authors.Create(ctx, author); err != nil {
			t.Fatal(err)
		}
		s := &models.Series{ForeignID: "hc-series:" + seriesTitle, Title: seriesTitle}
		if err := series.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			b := &models.Book{
				ForeignID: "hc:" + seriesTitle + ":" + r.title, AuthorID: author.ID, Title: r.title, SortTitle: r.title,
				Status: r.status, Monitored: r.monitored, MediaType: models.MediaTypeEbook,
				Genres: []string{}, MetadataProvider: "hardcover", OwnerUserID: r.owner,
			}
			if err := books.Create(ctx, b); err != nil {
				t.Fatal(err)
			}
			if r.excluded {
				if _, err := database.ExecContext(ctx, "UPDATE books SET excluded = 1 WHERE id = ?", b.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := series.LinkBook(ctx, s.ID, b.ID, r.position, true); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, b.ID)
		}
		return s.ID, ids
	}

	// Covered: the whole is wanted but monitored, so the library is already
	// looking for it.
	wantedID, wanted := seed("wanted-whole",
		row{"The Way of Kings", "1", models.BookStatusWanted, true, false, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true, false, alice.ID})
	// Not covered: the whole is excluded.
	_, excluded := seed("excluded-whole",
		row{"The Way of Kings", "1", models.BookStatusImported, true, true, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true, false, alice.ID})
	// Not covered: the whole is wanted and unmonitored, so nothing is being
	// sought and the parts may be what the user wants.
	_, unwanted := seed("unwanted-whole",
		row{"The Way of Kings", "1", models.BookStatusWanted, false, false, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true, false, alice.ID})
	// Not covered: alice owns the whole, the part is bob's.
	_, crossUser := seed("cross-user",
		row{"The Way of Kings", "1", models.BookStatusImported, true, false, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true, false, bob.ID})
	// Not reported: the part is already imported.
	_, importedPart := seed("imported-part",
		row{"The Way of Kings", "1", models.BookStatusImported, true, false, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusImported, true, false, alice.ID})

	parts, err := books.ListCoveredSplitEditionParts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].BookID != wanted[1] || parts[0].WholeBookID != wanted[0] || !parts[0].Monitored {
		t.Fatalf("parts = %+v, want only book %d covered by %d", parts, wanted[1], wanted[0])
	}
	for name, ids := range map[string][]int64{"excluded": excluded, "unwanted": unwanted, "cross user": crossUser, "imported part": importedPart} {
		for _, p := range parts {
			if p.BookID == ids[1] {
				t.Errorf("%s case: part %d reported as covered", name, ids[1])
			}
		}
	}

	scoped, err := books.ListCoveredSplitEditionParts(ctx, wantedID+100)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 0 {
		t.Errorf("a series filter matched other series: %+v", scoped)
	}

	n, err := books.UnmonitorNotImported(ctx, []int64{wanted[1], importedPart[1]})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("UnmonitorNotImported changed %d rows, want 1 (the imported part is left alone)", n)
	}
}

// TestListCoveredSplitEditionParts_AcrossSeries is #3324, seeded from the
// live case: Hardcover lists Part 2 of The Eye of the World beside the whole
// in The Wheel of Time (hc-series:1097) at 1.2, but Part 1 only in The Wheel
// of Time (Finnish Numbering) (hc-series:248644), where every part is its own
// volume and sits at the integer position 1.
func TestListCoveredSplitEditionParts_AcrossSeries(t *testing.T) {
	f := newSplitPartsFixture(t)
	jordan := f.author("hc:robert-jordan")
	wot := f.series("hc-series:1097", "The Wheel of Time")
	finnish := f.series("hc-series:248644", "The Wheel of Time (Finnish Numbering)")

	whole := f.book(jordan, "The Eye of the World", models.BookStatusImported, true, 0)
	f.link(wot, whole, "1")
	part2 := f.book(jordan, "The Eye of the World, Part 2 of 2: To The Blight", models.BookStatusWanted, true, 0)
	f.link(wot, part2, "1.2")
	part1 := f.book(jordan, "The Eye of the World, Part 1 of 2: From The Two Rivers", models.BookStatusWanted, true, 0)
	f.link(finnish, part1, "1")

	parts, err := f.books.ListCoveredSplitEditionParts(f.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[int64]SplitEditionPart, len(parts))
	for _, p := range parts {
		got[p.BookID] = p
	}
	if p, ok := got[part2]; !ok || p.SeriesID != wot || p.WholeBookID != whole {
		t.Errorf("part 2 (same series, 1.2) = %+v, %v; want covered by %d in series %d", p, ok, whole, wot)
	}
	if p, ok := got[part1]; !ok || p.SeriesID != finnish || p.WholeBookID != whole || !p.Monitored {
		t.Errorf("part 1 (other series, position 1) = %+v, %v; want covered by %d in series %d", p, ok, whole, finnish)
	}
	if len(parts) != 2 {
		t.Errorf("parts = %+v, want exactly the two Eye of the World parts", parts)
	}

	scoped, err := f.books.ListCoveredSplitEditionParts(f.ctx, finnish)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].BookID != part1 {
		t.Errorf("scoped to the Finnish series = %+v, want only part 1", scoped)
	}
}

// TestListCoveredSplitEditionParts_AcrossSeriesGuards pins what the
// title-only cross-series rule must not pair. Each case is its own author so
// no case can cover another's part.
func TestListCoveredSplitEditionParts_AcrossSeriesGuards(t *testing.T) {
	f := newSplitPartsFixture(t)
	bob, err := f.users.Create(f.ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}
	const wholeTitle, partTitle = "The Eye of the World", "The Eye of the World, Part 1 of 2: From The Two Rivers"

	type tc struct {
		name  string
		setup func(name string) int64 // returns the part id
	}
	cases := []tc{
		{"different author", func(n string) int64 {
			a, b := f.author("hc:"+n+"-a"), f.author("hc:"+n+"-b")
			f.link(f.series("s:"+n+"-a", n+" a"), f.book(a, wholeTitle, models.BookStatusImported, true, 0), "1")
			p := f.book(b, partTitle, models.BookStatusWanted, true, 0)
			f.link(f.series("s:"+n+"-b", n+" b"), p, "1")
			return p
		}},
		{"different owner", func(n string) int64 {
			a := f.author("hc:" + n)
			f.link(f.series("s:"+n+"-a", n+" a"), f.book(a, wholeTitle, models.BookStatusImported, true, 0), "1")
			p := f.book(a, partTitle, models.BookStatusWanted, true, bob.ID)
			f.link(f.series("s:"+n+"-b", n+" b"), p, "1")
			return p
		}},
		{"shares a series row, integer position", func(n string) int64 {
			a := f.author("hc:" + n)
			s := f.series("s:"+n, n)
			f.link(s, f.book(a, wholeTitle, models.BookStatusImported, true, 0), "1")
			p := f.book(a, partTitle, models.BookStatusWanted, true, 0)
			f.link(s, p, "3")
			return p
		}},
		{"no part marker", func(n string) int64 {
			a := f.author("hc:" + n)
			f.link(f.series("s:"+n+"-a", n+" a"), f.book(a, wholeTitle, models.BookStatusImported, true, 0), "1")
			p := f.book(a, "The Eye of the World: The Graphic Novel, Volume One", models.BookStatusWanted, true, 0)
			f.link(f.series("s:"+n+"-b", n+" b"), p, "1")
			return p
		}},
		{"whole excluded", func(n string) int64 {
			a := f.author("hc:" + n)
			w := f.book(a, wholeTitle, models.BookStatusImported, true, 0)
			f.exclude(w)
			f.link(f.series("s:"+n+"-a", n+" a"), w, "1")
			p := f.book(a, partTitle, models.BookStatusWanted, true, 0)
			f.link(f.series("s:"+n+"-b", n+" b"), p, "1")
			return p
		}},
		{"whole wanted and unmonitored", func(n string) int64 {
			a := f.author("hc:" + n)
			f.link(f.series("s:"+n+"-a", n+" a"), f.book(a, wholeTitle, models.BookStatusWanted, false, 0), "1")
			p := f.book(a, partTitle, models.BookStatusWanted, true, 0)
			f.link(f.series("s:"+n+"-b", n+" b"), p, "1")
			return p
		}},
		{"shares a series with a second copy of the whole", func(n string) int64 {
			a := f.author("hc:" + n)
			s := f.series("s:"+n, n)
			f.link(s, f.book(a, wholeTitle, models.BookStatusImported, true, 0), "1")
			p := f.book(a, partTitle, models.BookStatusWanted, true, 0)
			f.link(s, p, "3")
			// Another row with the whole's title, in no series shared with the part.
			f.link(f.series("s:"+n+"-other", n+" other"), f.book(a, wholeTitle, models.BookStatusImported, true, 0), "1")
			return p
		}},
		{"whole in no series", func(n string) int64 {
			a := f.author("hc:" + n)
			f.book(a, wholeTitle, models.BookStatusImported, true, 0)
			p := f.book(a, partTitle, models.BookStatusWanted, true, 0)
			f.link(f.series("s:"+n+"-b", n+" b"), p, "1")
			return p
		}},
		{"part already imported", func(n string) int64 {
			a := f.author("hc:" + n)
			f.link(f.series("s:"+n+"-a", n+" a"), f.book(a, wholeTitle, models.BookStatusImported, true, 0), "1")
			p := f.book(a, partTitle, models.BookStatusImported, true, 0)
			f.link(f.series("s:"+n+"-b", n+" b"), p, "1")
			return p
		}},
	}
	partIDs := make(map[int64]string, len(cases))
	for i, c := range cases {
		partIDs[c.setup(fmt.Sprintf("guard-%d", i))] = c.name
	}
	// Positive control in the same fixture, so a cross-series branch that
	// matched nothing at all could not pass every guard.
	ctrl := f.author("hc:control")
	f.link(f.series("s:control-a", "control a"), f.book(ctrl, wholeTitle, models.BookStatusImported, true, 0), "1")
	control := f.book(ctrl, partTitle, models.BookStatusWanted, true, 0)
	f.link(f.series("s:control-b", "control b"), control, "1")

	parts, err := f.books.ListCoveredSplitEditionParts(f.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	sawControl := false
	for _, p := range parts {
		if p.BookID == control {
			sawControl = true
		}
		if name, ok := partIDs[p.BookID]; ok {
			t.Errorf("%s: part %d reported as covered by %d", name, p.BookID, p.WholeBookID)
		}
	}
	if !sawControl {
		t.Errorf("control: cross-series part %d not reported; parts = %+v", control, parts)
	}
}

type splitPartsFixture struct {
	t          *testing.T
	ctx        context.Context
	db         *sql.DB
	users      *UserRepo
	authors    *AuthorRepo
	books      *BookRepo
	seriesRepo *SeriesRepo
	n          int
}

func newSplitPartsFixture(t *testing.T) *splitPartsFixture {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &splitPartsFixture{
		t: t, ctx: context.Background(), db: database,
		users: NewUserRepo(database), authors: NewAuthorRepo(database),
		books: NewBookRepo(database), seriesRepo: NewSeriesRepo(database),
	}
}

func (f *splitPartsFixture) author(foreignID string) int64 {
	f.t.Helper()
	a := &models.Author{ForeignID: foreignID, Name: foreignID, SortName: foreignID, MetadataProvider: "hardcover"}
	if err := f.authors.Create(f.ctx, a); err != nil {
		f.t.Fatal(err)
	}
	return a.ID
}

func (f *splitPartsFixture) series(foreignID, title string) int64 {
	f.t.Helper()
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := f.seriesRepo.Create(f.ctx, s); err != nil {
		f.t.Fatal(err)
	}
	return s.ID
}

func (f *splitPartsFixture) link(seriesID, bookID int64, position string) {
	f.t.Helper()
	if err := f.seriesRepo.LinkBook(f.ctx, seriesID, bookID, position, true); err != nil {
		f.t.Fatal(err)
	}
}

func (f *splitPartsFixture) book(authorID int64, title, status string, monitored bool, owner int64) int64 {
	f.t.Helper()
	f.n++
	b := &models.Book{
		ForeignID: fmt.Sprintf("hc:book-%d", f.n), AuthorID: authorID, Title: title, SortTitle: title,
		Status: status, Monitored: monitored, MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "hardcover", OwnerUserID: owner,
	}
	if err := f.books.Create(f.ctx, b); err != nil {
		f.t.Fatal(err)
	}
	return b.ID
}

func (f *splitPartsFixture) exclude(bookID int64) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, "UPDATE books SET excluded = 1 WHERE id = ?", bookID); err != nil {
		f.t.Fatal(err)
	}
}
