package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

type mergeFixture struct {
	t      *testing.T
	ctx    context.Context
	db     *sql.DB
	series *SeriesRepo
	books  []int64
}

func newMergeFixture(t *testing.T, nBooks int) *mergeFixture {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &mergeFixture{t: t, ctx: context.Background(), db: database, series: NewSeriesRepo(database)}
	author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "openlibrary"}
	if err := NewAuthorRepo(database).Create(f.ctx, author); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	for i := 1; i <= nBooks; i++ {
		b := &models.Book{ForeignID: fmt.Sprintf("B%d", i), AuthorID: author.ID, Title: fmt.Sprintf("Bok %d", i),
			SortTitle: fmt.Sprintf("bok %d", i), Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary"}
		if err := books.Create(f.ctx, b); err != nil {
			t.Fatal(err)
		}
		f.books = append(f.books, b.ID)
	}
	return f
}

func (f *mergeFixture) newSeries(foreignID, title string) int64 {
	f.t.Helper()
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := f.series.CreateOrGet(f.ctx, s); err != nil {
		f.t.Fatal(err)
	}
	return s.ID
}

func (f *mergeFixture) link(seriesID int64, book int, position string, primary bool) {
	f.t.Helper()
	if _, err := f.db.Exec(`INSERT INTO series_books (series_id, book_id, position_in_series, primary_series) VALUES (?, ?, ?, ?)`,
		seriesID, f.books[book-1], position, boolToInt(primary)); err != nil {
		f.t.Fatal(err)
	}
}

// membership returns book -> "position/P" (P when primary) for a series.
func (f *mergeFixture) membership(seriesID int64) map[int]string {
	f.t.Helper()
	rows, err := f.db.Query(`SELECT book_id, position_in_series, primary_series FROM series_books WHERE series_id = ?`, seriesID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var id int64
		var pos string
		var primary int
		if err := rows.Scan(&id, &pos, &primary); err != nil {
			f.t.Fatal(err)
		}
		for i, b := range f.books {
			if b == id {
				out[i+1] = pos + map[int]string{0: "", 1: "/P"}[primary]
			}
		}
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *mergeFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// The worked case: two sources merged into a target, covering position fill
// and conflicts, the primary flag, the Hardcover link, genre override,
// monitoring, renaming and aliases (#2554).
func TestSeriesMerge(t *testing.T) {
	f := newMergeFixture(t, 4)
	target := f.newSeries("nb-series:1:fjellserien", "Fjellserien")
	src1 := f.newSeries("nb-series:1:serien-om-fjellet", "Serien om fjellet")
	src2 := f.newSeries("nb-series:1:fjell", "Fjell")

	f.link(target, 1, "1", true)
	f.link(target, 2, "", true)
	f.link(src1, 2, "2", false) // fills the target's empty position
	f.link(src1, 3, "3", true)  // moves, primary
	f.link(src2, 3, "4", false) // conflicts with the position src1 brought
	f.link(src2, 4, "5", true)

	addSeriesAlias(t, f.series, "nb-series:1:eldre", src1) // carried over to the target
	if err := f.series.UpsertHardcoverLink(f.ctx, &models.SeriesHardcoverLink{SeriesID: src1, HardcoverSeriesID: "hc-series:9", LinkedBy: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := f.series.SetGenreOverride(f.ctx, src1, []string{"Krim"}); err != nil {
		t.Fatal(err)
	}
	if err := f.series.SetMonitored(f.ctx, src2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO author_monitored_series (author_id, series_id, created_at) SELECT id, ?, CURRENT_TIMESTAMP FROM authors`, src2); err != nil {
		t.Fatal(err)
	}

	plan, err := f.series.Merge(f.ctx, target, []int64{src2, src1}, "Fjell-serien")
	if err != nil {
		t.Fatal(err)
	}

	want := map[int]string{1: "1/P", 2: "2/P", 3: "3/P", 4: "5/P"}
	if got := f.membership(target); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("target books = %v, want %v", got, want)
	}
	if len(plan.Sources) != 2 || plan.Sources[0].ID != src1 || len(plan.Sources[1].Conflicts) != 1 ||
		plan.Sources[1].Conflicts[0].TargetPosition != "3" || plan.Sources[1].Conflicts[0].SourcePosition != "4" {
		t.Errorf("plan sources = %+v", plan.Sources)
	}
	if n := f.count(`SELECT COUNT(*) FROM series WHERE id IN (?, ?)`, src1, src2); n != 0 {
		t.Errorf("%d source series left", n)
	}
	for _, id := range []string{"nb-series:1:serien-om-fjellet", "nb-series:1:fjell", "nb-series:1:eldre"} {
		s := &models.Series{ForeignID: id, Title: "x"}
		if err := f.series.CreateOrGet(f.ctx, s); err != nil || s.ID != target {
			t.Errorf("CreateOrGet(%s) = %d, %v; want the target %d", id, s.ID, err, target)
		}
	}
	got, err := f.series.GetByID(f.ctx, target)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Title != "Fjell-serien" || !got.Monitored || !got.GenreOverrideSet || fmt.Sprint(got.GenreOverride) != "[Krim]" {
		t.Errorf("target = %+v", got)
	}
	if link, err := f.series.GetHardcoverLink(f.ctx, target); err != nil || link == nil || link.HardcoverSeriesID != "hc-series:9" {
		t.Errorf("target hardcover link = %+v, %v", link, err)
	}
	if n := f.count(`SELECT COUNT(*) FROM author_monitored_series WHERE series_id = ?`, target); n != 1 {
		t.Errorf("author monitoring rows on target = %d, want 1", n)
	}
	if n := f.count(`SELECT COUNT(*) FROM series_books WHERE book_id = ? AND primary_series = 1`, f.books[2]); n != 1 {
		t.Errorf("book 3 has %d primary series, want 1", n)
	}
}

// A dry run reports the plan and changes nothing.
func TestSeriesPlanMergeChangesNothing(t *testing.T) {
	f := newMergeFixture(t, 2)
	target := f.newSeries("s:t", "T")
	src := f.newSeries("s:s", "S")
	f.link(target, 1, "1", true)
	f.link(src, 2, "2", true)

	plan, err := f.series.PlanMerge(f.ctx, target, []int64{src}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sources) != 1 || len(plan.Sources[0].Moved) != 1 || plan.Title != "T" || fmt.Sprint(plan.Aliases) != "[s:s]" {
		t.Errorf("plan = %+v", plan)
	}
	if n := f.count(`SELECT COUNT(*) FROM series`); n != 2 {
		t.Errorf("series = %d after a dry run, want 2", n)
	}
	if n := f.count(`SELECT COUNT(*) FROM series_aliases`); n != 0 {
		t.Errorf("aliases = %d after a dry run, want 0", n)
	}
}

// A book whose primary series is a third one keeps it.
func TestSeriesMergeKeepsPrimaryElsewhere(t *testing.T) {
	f := newMergeFixture(t, 1)
	target := f.newSeries("s:t", "T")
	src := f.newSeries("s:s", "S")
	other := f.newSeries("s:o", "Other")
	f.link(other, 1, "1", true)
	f.link(src, 1, "7", false)

	if _, err := f.series.Merge(f.ctx, target, []int64{src}, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.membership(target); got[1] != "7" {
		t.Errorf("target = %v, want book 1 at 7, not primary", got)
	}
	if got := f.membership(other); got[1] != "1/P" {
		t.Errorf("other = %v, want book 1 still primary there", got)
	}
}

// Merging A into B and then B into C leaves A's id resolving to C.
func TestSeriesMergeChains(t *testing.T) {
	f := newMergeFixture(t, 0)
	a, b, c := f.newSeries("s:a", "A"), f.newSeries("s:b", "B"), f.newSeries("s:c", "C")
	if _, err := f.series.Merge(f.ctx, b, []int64{a}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.Merge(f.ctx, c, []int64{b}, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s:a", "s:b"} {
		if s, err := f.series.GetByForeignID(f.ctx, id); err != nil || s == nil || s.ID != c {
			t.Errorf("GetByForeignID(%s) = %+v, %v; want series %d", id, s, err, c)
		}
	}
}

// A hand-made series' synthetic id never comes back from a provider, so
// merging one leaves no alias behind, in the plan or in the table.
func TestSeriesMergeDropsManualID(t *testing.T) {
	f := newMergeFixture(t, 0)
	target := f.newSeries("s:t", "T")
	manual, err := f.series.CreateManual(f.ctx, "Handmade")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.series.PlanMerge(f.ctx, target, []int64{manual.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Aliases) != 0 {
		t.Errorf("plan aliases = %v, want none", plan.Aliases)
	}
	if _, err := f.series.Merge(f.ctx, target, []int64{manual.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM series_aliases`); n != 0 {
		t.Errorf("series_aliases rows = %d, want 0", n)
	}
}

func TestSeriesMergeRejectsBadRequests(t *testing.T) {
	f := newMergeFixture(t, 0)
	target, src := f.newSeries("s:t", "T"), f.newSeries("s:s", "S")
	for name, sources := range map[string][]int64{
		"no sources":     nil,
		"target listed":  {src, target},
		"listed twice":   {src, src},
		"missing source": {src, 999},
	} {
		if _, err := f.series.Merge(f.ctx, target, sources, ""); !errors.Is(err, ErrSeriesMergeInvalid) {
			t.Errorf("%s: err = %v, want ErrSeriesMergeInvalid", name, err)
		}
	}
	if _, err := f.series.Merge(f.ctx, 999, []int64{src}, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing target: err = %v, want sql.ErrNoRows", err)
	}
	if n := f.count(`SELECT COUNT(*) FROM series`); n != 2 {
		t.Errorf("series = %d after rejected merges, want 2", n)
	}
}

// A merge is one transaction: when a late step fails (here the alias insert,
// after books have moved and the source is deleted), nothing is left half
// done.
func TestSeriesMergeRollsBackOnFailure(t *testing.T) {
	f := newMergeFixture(t, 2)
	target, src := f.newSeries("s:t", "T"), f.newSeries("s:s", "S")
	f.link(target, 1, "1", true)
	f.link(src, 2, "2", true)
	if _, err := f.db.Exec(`CREATE TEMP TRIGGER fail_alias BEFORE INSERT ON series_aliases BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}

	_, err := f.series.Merge(f.ctx, target, []int64{src}, "Renamed")
	if err == nil || !strings.Contains(err.Error(), "alias the merged series' id") {
		t.Fatalf("err = %v, want the failing step named", err)
	}
	if got := f.membership(target); fmt.Sprint(got) != "map[1:1/P]" {
		t.Errorf("target books = %v, want unchanged", got)
	}
	if got := f.membership(src); fmt.Sprint(got) != "map[2:2/P]" {
		t.Errorf("source books = %v, want unchanged", got)
	}
	if s, _ := f.series.GetByID(f.ctx, target); s == nil || s.Title != "T" {
		t.Errorf("target = %+v, want the title unchanged", s)
	}
}

// A failed read while planning is an error, not an empty plan.
func TestSeriesPlanMergeReadError(t *testing.T) {
	f := newMergeFixture(t, 0)
	target, src := f.newSeries("s:t", "T"), f.newSeries("s:s", "S")
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if plan, err := f.series.PlanMerge(ctx, target, []int64{src}, ""); err == nil || plan != nil {
		t.Errorf("PlanMerge on a cancelled context = %+v, %v; want an error", plan, err)
	}
}
