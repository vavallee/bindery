package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// addAuthorBook creates a book by a new or existing author (by name) and
// returns its id.
func (f *mergeFixture) addAuthorBook(author string) int64 {
	f.t.Helper()
	authors := NewAuthorRepo(f.db)
	var id int64
	if err := f.db.QueryRow(`SELECT id FROM authors WHERE name = ?`, author).Scan(&id); err != nil {
		a := &models.Author{ForeignID: "OL-" + author, Name: author, SortName: author, MetadataProvider: "openlibrary"}
		if err := authors.Create(f.ctx, a); err != nil {
			f.t.Fatal(err)
		}
		id = a.ID
	}
	n := len(f.books) + 1
	b := &models.Book{ForeignID: fmt.Sprintf("DB%d", n), AuthorID: id, Title: fmt.Sprintf("Dup %d", n),
		SortTitle: fmt.Sprintf("dup %d", n), Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := NewBookRepo(f.db).Create(f.ctx, b); err != nil {
		f.t.Fatal(err)
	}
	f.books = append(f.books, b.ID)
	return b.ID
}

func (f *mergeFixture) linkBookID(seriesID, bookID int64) {
	f.t.Helper()
	if _, err := f.db.Exec(`INSERT INTO series_books (series_id, book_id, position_in_series, primary_series) VALUES (?, ?, '', 0)`, seriesID, bookID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *mergeFixture) hardcoverLink(seriesID int64, hc string) {
	f.t.Helper()
	if _, err := f.db.Exec(`INSERT INTO series_hardcover_links (series_id, hardcover_series_id) VALUES (?, ?)`, seriesID, hc); err != nil {
		f.t.Fatal(err)
	}
}

func groupFor(t *testing.T, groups []TitleDuplicateGroup, id int64) *TitleDuplicateGroup {
	t.Helper()
	for i := range groups {
		for _, s := range groups[i].Series {
			if s.ID == id {
				return &groups[i]
			}
		}
	}
	return nil
}

// Titles that differ only in case, punctuation and the filing form of the
// article share the identity key and group; a different title does not.
func TestListTitleDuplicates_GroupsByIdentityKey(t *testing.T) {
	f := newMergeFixture(t, 0)
	a := f.newSeries("calibre-series:1", "The Stormlight Archive")
	b := f.newSeries("abs-series:1", "the stormlight archive!")
	c := f.newSeries("abs-series:2", "Stormlight Archive, The")
	other := f.newSeries("abs-series:3", "Mistborn")

	groups, err := f.series.ListTitleDuplicates(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v, want one", groups)
	}
	g := groups[0]
	if len(g.Series) != 3 {
		t.Fatalf("group series = %+v, want three", g.Series)
	}
	ids := map[int64]bool{}
	for _, s := range g.Series {
		ids[s.ID] = true
	}
	if !ids[a] || !ids[b] || !ids[c] || ids[other] {
		t.Errorf("group ids = %v", ids)
	}
	if !g.SameAuthors {
		t.Error("empty series should count as compatible")
	}
	// All empty, none linked: the lowest id is kept, and listed first.
	if g.SuggestedKeepID != a || g.Series[0].ID != a {
		t.Errorf("keep = %d (first %d), want %d", g.SuggestedKeepID, g.Series[0].ID, a)
	}
}

func TestListTitleDuplicates_SuggestedKeepOrder(t *testing.T) {
	f := newMergeFixture(t, 0)
	// Most books wins over the lowest id.
	low := f.newSeries("calibre-series:1", "Wayfarers")
	more := f.newSeries("abs-series:1", "Wayfarers")
	f.linkBookID(more, f.addAuthorBook("Becky Chambers"))
	f.linkBookID(more, f.addAuthorBook("Becky Chambers"))
	f.linkBookID(low, f.addAuthorBook("Becky Chambers"))

	// A Hardcover link wins over more books.
	big := f.newSeries("calibre-series:2", "Murderbot")
	linked := f.newSeries("abs-series:2", "Murderbot")
	f.linkBookID(big, f.addAuthorBook("Martha Wells"))
	f.linkBookID(big, f.addAuthorBook("Martha Wells"))
	f.linkBookID(linked, f.addAuthorBook("Martha Wells"))
	f.hardcoverLink(linked, "hc-series:7")

	groups, err := f.series.ListTitleDuplicates(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupFor(t, groups, low); g == nil || g.SuggestedKeepID != more {
		t.Errorf("wayfarers group = %+v, want keep %d (most books)", g, more)
	}
	g := groupFor(t, groups, big)
	if g == nil || g.SuggestedKeepID != linked {
		t.Fatalf("murderbot group = %+v, want keep %d (linked)", g, linked)
	}
	if !g.Series[0].HardcoverLinked || g.Series[0].BookCount != 1 || g.Series[1].BookCount != 2 {
		t.Errorf("murderbot series = %+v", g.Series)
	}
	if len(g.Series[0].Authors) != 1 || g.Series[0].Authors[0] != "Martha Wells" {
		t.Errorf("authors = %v", g.Series[0].Authors)
	}
}

// Series holding books by different authors are still a group, flagged as
// probably two series with one name. An empty member does not change that,
// and does not hide it when the keep itself is empty.
func TestListTitleDuplicates_DisjointAuthorsFlagged(t *testing.T) {
	f := newMergeFixture(t, 0)
	one := f.newSeries("calibre-series:1", "Legacy")
	two := f.newSeries("abs-series:1", "Legacy")
	empty := f.newSeries("abs-series:2", "LEGACY")
	f.linkBookID(one, f.addAuthorBook("Author One"))
	f.linkBookID(two, f.addAuthorBook("Author Two"))

	// An empty but linked keep: the comparison falls to the fullest series.
	lk := f.newSeries("abs-series:3", "Echoes")
	e1 := f.newSeries("calibre-series:3", "Echoes")
	e2 := f.newSeries("calibre-series:4", "echoes")
	f.hardcoverLink(lk, "hc-series:9")
	f.linkBookID(e1, f.addAuthorBook("Author One"))
	f.linkBookID(e2, f.addAuthorBook("Author Two"))

	// Shared author among others counts as the same series.
	s1 := f.newSeries("calibre-series:5", "Dune")
	s2 := f.newSeries("abs-series:5", "Dune")
	f.linkBookID(s1, f.addAuthorBook("Frank Herbert"))
	f.linkBookID(s2, f.addAuthorBook("Frank Herbert"))
	f.linkBookID(s2, f.addAuthorBook("Brian Herbert"))

	groups, err := f.series.ListTitleDuplicates(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupFor(t, groups, empty); g == nil || g.SameAuthors || len(g.Series) != 3 {
		t.Errorf("legacy group = %+v, want three series and sameAuthors false", g)
	}
	if g := groupFor(t, groups, lk); g == nil || g.SameAuthors || g.SuggestedKeepID != lk {
		t.Errorf("echoes group = %+v, want keep %d and sameAuthors false", g, lk)
	}
	if g := groupFor(t, groups, s1); g == nil || !g.SameAuthors {
		t.Errorf("dune group = %+v, want sameAuthors true", g)
	}
}

// Sub series that share a base title and an author but carry different
// qualifiers are two series, not duplicates. LookupKey drops the qualifier, so
// before the finder grouped by IdentityKey these were offered as one group
// with matching authors, and "Merge all" would have merged them (#2173).
func TestListTitleDuplicates_KeepsQualifiers(t *testing.T) {
	f := newMergeFixture(t, 0)
	cityWatch := f.newSeries("abs-series:1", "Discworld (City Watch)")
	witches := f.newSeries("abs-series:2", "Discworld (Witches)")
	twin := f.newSeries("calibre:series:Discworld (Witches)", "discworld: witches")
	legends := f.newSeries("abs-series:3", "Star Wars [Legends]")
	canon := f.newSeries("abs-series:4", "Star Wars (Canon)")
	for _, id := range []int64{cityWatch, witches, twin} {
		f.linkBookID(id, f.addAuthorBook("Terry Pratchett"))
	}
	for _, id := range []int64{legends, canon} {
		f.linkBookID(id, f.addAuthorBook("Timothy Zahn"))
	}

	groups, err := f.series.ListTitleDuplicates(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v, want only the Witches twins", groups)
	}
	g := groupFor(t, groups, witches)
	if g == nil || len(g.Series) != 2 || groupFor(t, groups, twin) != g {
		t.Fatalf("Witches group = %+v, want the two Witches rows", g)
	}
	for _, id := range []int64{cityWatch, legends, canon} {
		if groupFor(t, groups, id) != nil {
			t.Errorf("series %d was grouped with a sub series of another qualifier", id)
		}
	}
}

func TestListTitleDuplicates_NoneIsEmptyList(t *testing.T) {
	f := newMergeFixture(t, 0)
	f.newSeries("abs-series:1", "Alone")
	groups, err := f.series.ListTitleDuplicates(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if groups == nil || len(groups) != 0 {
		t.Errorf("groups = %#v, want an empty non nil list", groups)
	}
}

// A merge makes the ABS import's ambiguous series matches due again, and
// leaves matched and miss rows, and a dry run, alone (#2173).
func TestMergeMakesAmbiguousABSSeriesMatchesDue(t *testing.T) {
	f := newMergeFixture(t, 0)
	keep := f.newSeries("calibre-series:1", "Wayfarers")
	src := f.newSeries("abs-series:1", "Wayfarers")
	states := NewABSSeriesMatchRepo(f.db)
	recent := time.Now().UTC().Add(-time.Hour)
	for item, outcome := range map[string]string{"amb": ABSSeriesMatchOutcomeAmbiguous, "hit": "matched", "gone": "miss"} {
		if err := states.Upsert(f.ctx, &models.ABSSeriesMatchState{SourceID: "s", LibraryID: "l", ItemID: item, InputHash: "h", Outcome: outcome}, recent); err != nil {
			t.Fatal(err)
		}
	}
	checked := func(item string) time.Time {
		t.Helper()
		s, err := states.Get(f.ctx, "s", "l", item)
		if err != nil || s == nil {
			t.Fatalf("state %s = %+v, %v", item, s, err)
		}
		return s.CheckedAt
	}

	if _, err := f.series.PlanMerge(f.ctx, keep, []int64{src}, ""); err != nil {
		t.Fatal(err)
	}
	if !checked("amb").After(recent.Add(-time.Minute)) {
		t.Error("a dry run touched the ambiguous row")
	}
	if _, err := f.series.Merge(f.ctx, keep, []int64{src}, ""); err != nil {
		t.Fatal(err)
	}
	if got := checked("amb"); got.After(time.Unix(0, 0).UTC().Add(time.Hour)) {
		t.Errorf("ambiguous row checked at %v, want the epoch", got)
	}
	for _, item := range []string{"hit", "gone"} {
		if got := checked(item); got.Before(recent.Add(-time.Minute)) {
			t.Errorf("%s row checked at %v, want it left at %v", item, got, recent)
		}
	}
}

// A merge of differently titled series resolves no collision the ABS import
// could have hit, so it leaves ambiguous rows to their schedule (#2173).
func TestMergeOfDifferentTitlesLeavesAmbiguousABSSeriesMatches(t *testing.T) {
	f := newMergeFixture(t, 0)
	keep := f.newSeries("calibre-series:1", "Wayfarers")
	src := f.newSeries("abs-series:1", "Galactic Commons")
	states := NewABSSeriesMatchRepo(f.db)
	recent := time.Now().UTC().Add(-time.Hour)
	if err := states.Upsert(f.ctx, &models.ABSSeriesMatchState{SourceID: "s", LibraryID: "l", ItemID: "amb", InputHash: "h", Outcome: ABSSeriesMatchOutcomeAmbiguous}, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.Merge(f.ctx, keep, []int64{src}, ""); err != nil {
		t.Fatal(err)
	}
	s, err := states.Get(f.ctx, "s", "l", "amb")
	if err != nil || s == nil {
		t.Fatalf("state = %+v, %v", s, err)
	}
	if s.CheckedAt.Before(recent.Add(-time.Minute)) {
		t.Errorf("ambiguous row checked at %v after a merge of different titles, want it left at %v", s.CheckedAt, recent)
	}
}
