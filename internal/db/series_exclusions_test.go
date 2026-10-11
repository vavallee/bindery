package db

import (
	"context"
	"fmt"
	"testing"
)

// A book the user took out of a series stays out: no automatic path files it
// back under that series (#2554), until the user adds it back by hand.
func TestBookSeriesExclusion(t *testing.T) {
	f := newMergeFixture(t, 1)
	s := f.newSeries("s:fjell", "Fjellserien")
	f.link(s, 1, "1", true)

	removed, err := f.series.RemoveBookFromSeries(f.ctx, s, f.books[0])
	if err != nil || !removed {
		t.Fatalf("RemoveBookFromSeries = %v, %v", removed, err)
	}
	created, err := f.series.LinkBookIfMissing(f.ctx, s, f.books[0], "1", true)
	if err != nil || created {
		t.Fatalf("an automatic link after the user removed the book = %v, %v; want skipped", created, err)
	}
	if created, err := f.series.LinkBookPreservingPrimary(f.ctx, s, f.books[0], "1"); err != nil || created {
		t.Fatalf("LinkBookPreservingPrimary = %v, %v; want skipped", created, err)
	}
	if got := f.membership(s); len(got) != 0 {
		t.Fatalf("membership = %v, want the book kept out", got)
	}

	if err := f.series.UpsertBookLink(f.ctx, s, f.books[0], "2", true); err != nil {
		t.Fatal(err)
	}
	if got := f.membership(s); fmt.Sprint(got) != "map[1:2/P]" {
		t.Errorf("after adding it back by hand = %v, want the book at 2", got)
	}
	if n := f.count(`SELECT COUNT(*) FROM book_series_exclusions`); n != 0 {
		t.Errorf("exclusions = %d after adding back, want 0", n)
	}
}

// The exclusion is by provider id, so it holds when the series is merged
// away: its id becomes an alias of the target, and the book stays out of it.
func TestBookSeriesExclusionFollowsMerge(t *testing.T) {
	f := newMergeFixture(t, 1)
	target, src := f.newSeries("s:t", "T"), f.newSeries("s:s", "S")
	f.link(src, 1, "1", true)
	if _, err := f.series.RemoveBookFromSeries(f.ctx, src, f.books[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.Merge(f.ctx, target, []int64{src}, ""); err != nil {
		t.Fatal(err)
	}
	if created, err := f.series.LinkBookIfMissing(f.ctx, target, f.books[0], "1", true); err != nil || created {
		t.Errorf("link into the merge target = %v, %v; want skipped, the book was taken out of the merged series", created, err)
	}
}

// A book taken out of a duplicate but still in the series it is merged into
// is in that series: the merge drops the exclusion instead of leaving the
// book listed as kept out of a series it is in. One kept out of the
// duplicate only still stays out.
func TestMergeDropsExclusionsForBooksInTheTarget(t *testing.T) {
	f := newMergeFixture(t, 2)
	target, src := f.newSeries("s:t", "T"), f.newSeries("s:s", "S")
	f.link(target, 1, "1", true)
	f.link(src, 1, "1", true)
	f.link(src, 2, "2", true)
	for _, b := range f.books {
		if _, err := f.series.RemoveBookFromSeries(f.ctx, src, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.series.Merge(f.ctx, target, []int64{src}, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := f.series.ListBookSeriesExclusions(f.ctx, f.books[0]); err != nil || len(got) != 0 {
		t.Errorf("book in the target: exclusions = %+v, %v; want none", got, err)
	}
	if got, err := f.series.ListBookSeriesExclusions(f.ctx, f.books[1]); err != nil || len(got) != 1 || got[0].SeriesID != target {
		t.Errorf("book only in the duplicate: exclusions = %+v, %v; want it kept out of the target", got, err)
	}
}

// A hand-made series' id is not kept as an alias, so what was kept out of it
// goes with it.
func TestMergeDropsAHandMadeSeriesExclusions(t *testing.T) {
	f := newMergeFixture(t, 1)
	target := f.newSeries("s:t", "T")
	manual, err := f.series.CreateManual(f.ctx, "Handmade")
	if err != nil {
		t.Fatal(err)
	}
	f.link(manual.ID, 1, "1", true)
	if _, err := f.series.RemoveBookFromSeries(f.ctx, manual.ID, f.books[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.Merge(f.ctx, target, []int64{manual.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM book_series_exclusions`); n != 0 {
		t.Errorf("exclusions = %d, want 0", n)
	}
}

// Removing a book that is not in the series records nothing.
func TestRemoveBookFromSeriesNotMember(t *testing.T) {
	f := newMergeFixture(t, 1)
	s := f.newSeries("s:fjell", "Fjellserien")
	if removed, err := f.series.RemoveBookFromSeries(f.ctx, s, f.books[0]); err != nil || removed {
		t.Fatalf("RemoveBookFromSeries = %v, %v; want false", removed, err)
	}
	if n := f.count(`SELECT COUNT(*) FROM book_series_exclusions`); n != 0 {
		t.Errorf("exclusions = %d, want 0", n)
	}
}

// Filing a book under a series as primary by hand demotes its other series,
// so it keeps one primary (#2525).
func TestUpsertBookLinkKeepsOnePrimary(t *testing.T) {
	f := newMergeFixture(t, 1)
	a, b := f.newSeries("s:a", "A"), f.newSeries("s:b", "B")
	f.link(a, 1, "1", true)
	if err := f.series.UpsertBookLink(f.ctx, b, f.books[0], "3", true); err != nil {
		t.Fatal(err)
	}
	if got := f.membership(a); got[1] != "1" {
		t.Errorf("A = %v, want book 1 kept but no longer primary", got)
	}
	if got := f.membership(b); got[1] != "3/P" {
		t.Errorf("B = %v, want book 1 primary at 3", got)
	}
	// Not primary leaves the existing primary alone.
	if err := f.series.UpsertBookLink(f.ctx, a, f.books[0], "1", false); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM series_books WHERE book_id = ? AND primary_series = 1`, f.books[0]); n != 1 {
		t.Errorf("primary series = %d, want 1", n)
	}
}

// The exclusion keeps the position the book had, and lists under the series'
// current name: after a merge, the series it was merged into.
func TestListBookSeriesExclusions(t *testing.T) {
	f := newMergeFixture(t, 1)
	target, src := f.newSeries("s:t", "Fjellserien"), f.newSeries("s:s", "Serien om fjellet")
	gone := f.newSeries("s:gone", "Borte")
	f.link(src, 1, "4", true)
	f.link(gone, 1, "", false)
	for _, s := range []int64{src, gone} {
		if _, err := f.series.RemoveBookFromSeries(f.ctx, s, f.books[0]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.series.Merge(f.ctx, target, []int64{src}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.series.Delete(f.ctx, gone); err != nil {
		t.Fatal(err)
	}

	got, err := f.series.ListBookSeriesExclusions(f.ctx, f.books[0])
	if err != nil {
		t.Fatal(err)
	}
	// By name; a series that no longer exists sorts by its id.
	want := []BookSeriesExclusion{
		{SeriesForeignID: "s:s", SeriesID: target, SeriesTitle: "Fjellserien", Position: "4"},
		{SeriesForeignID: "s:gone", SeriesID: 0, SeriesTitle: "", Position: ""},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("exclusions = %+v, want %+v", got, want)
	}

	if err := f.series.ClearBookSeriesExclusions(f.ctx, f.books[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.series.ListBookSeriesExclusions(f.ctx, f.books[0]); len(got) != 0 {
		t.Errorf("after clearing = %+v, want none", got)
	}
	if created, err := f.series.LinkBookIfMissing(f.ctx, target, f.books[0], "4", true); err != nil || !created {
		t.Errorf("automatic link after clearing = %v, %v; want linked again", created, err)
	}
}

// UpsertBookLink is one transaction: when its last step (demoting the book's
// other series) fails, the exclusion it cleared and the link it wrote are
// rolled back too.
func TestUpsertBookLinkIsAtomic(t *testing.T) {
	f := newMergeFixture(t, 1)
	a, b := f.newSeries("s:a", "A"), f.newSeries("s:b", "B")
	f.link(a, 1, "1", true)
	f.link(b, 1, "2", false)
	if _, err := f.series.RemoveBookFromSeries(f.ctx, b, f.books[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`CREATE TEMP TRIGGER fail_demote BEFORE UPDATE OF primary_series ON series_books
		WHEN NEW.primary_series = 0 BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.series.UpsertBookLink(f.ctx, b, f.books[0], "2", true); err == nil {
		t.Fatal("UpsertBookLink succeeded although its last step failed")
	}
	if got := f.membership(b); len(got) != 0 {
		t.Errorf("B = %v, want the link rolled back", got)
	}
	if got := f.membership(a); got[1] != "1/P" {
		t.Errorf("A = %v, want book 1 still primary", got)
	}
	if n := f.count(`SELECT COUNT(*) FROM book_series_exclusions`); n != 1 {
		t.Errorf("exclusions = %d, want the cleared one restored", n)
	}
}

// RemoveBookFromSeries is one transaction: when the removal fails, no
// exclusion is left behind for a book still in the series.
func TestRemoveBookFromSeriesIsAtomic(t *testing.T) {
	f := newMergeFixture(t, 1)
	s := f.newSeries("s:a", "A")
	f.link(s, 1, "1", true)
	if _, err := f.db.Exec(`CREATE TEMP TRIGGER fail_unlink BEFORE DELETE ON series_books BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.RemoveBookFromSeries(f.ctx, s, f.books[0]); err == nil {
		t.Fatal("RemoveBookFromSeries succeeded although the removal failed")
	}
	if got := f.membership(s); got[1] != "1/P" {
		t.Errorf("membership = %v, want the book still in the series", got)
	}
	if n := f.count(`SELECT COUNT(*) FROM book_series_exclusions`); n != 0 {
		t.Errorf("exclusions = %d, want none for a book still in the series", n)
	}
}

// Read and clear failures are errors, not an empty list or a silent no-op.
func TestBookSeriesExclusionFailures(t *testing.T) {
	f := newMergeFixture(t, 1)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if got, err := f.series.ListBookSeriesExclusions(ctx, f.books[0]); err == nil {
		t.Errorf("list on a cancelled context = %v, want an error", got)
	}
	if _, err := f.db.Exec(`CREATE TEMP TRIGGER fail_clear BEFORE DELETE ON book_series_exclusions BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO book_series_exclusions (book_id, series_foreign_id) VALUES (?, 's:x')`, f.books[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.series.ClearBookSeriesExclusions(f.ctx, f.books[0]); err == nil {
		t.Error("clear succeeded although the delete failed")
	}
}
