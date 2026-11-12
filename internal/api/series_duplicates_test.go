package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type dupEnv struct {
	t      *testing.T
	ctx    context.Context
	db     *sql.DB
	repo   *db.SeriesRepo
	h      *SeriesHandler
	author int64
	n      int
}

func newDupEnv(t *testing.T) *dupEnv {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	e := &dupEnv{t: t, ctx: context.Background(), db: database, repo: db.NewSeriesRepo(database)}
	a := &models.Author{ForeignID: "OL1A", Name: "Becky Chambers", SortName: "Chambers, Becky", MetadataProvider: "openlibrary"}
	if err := db.NewAuthorRepo(database).Create(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.author = a.ID
	e.h = NewSeriesHandler(e.repo, db.NewBookRepo(database), db.NewAuthorRepo(database), nil, nil)
	return e
}

func (e *dupEnv) series(foreignID, title string, books int) int64 {
	e.t.Helper()
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := e.repo.CreateOrGet(e.ctx, s); err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i < books; i++ {
		e.n++
		b := &models.Book{ForeignID: fmt.Sprintf("B%d", e.n), AuthorID: e.author, Title: fmt.Sprintf("Book %d", e.n),
			SortTitle: fmt.Sprintf("book %d", e.n), Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary"}
		if err := db.NewBookRepo(e.db).Create(e.ctx, b); err != nil {
			e.t.Fatal(err)
		}
		if _, err := e.db.Exec(`INSERT INTO series_books (series_id, book_id, position_in_series, primary_series) VALUES (?, ?, ?, 1)`,
			s.ID, b.ID, fmt.Sprint(i+1)); err != nil {
			e.t.Fatal(err)
		}
	}
	return s.ID
}

func (e *dupEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

type dupMergeResponse struct {
	DryRun  bool                   `json:"dryRun"`
	Merged  int                    `json:"merged"`
	Failed  int                    `json:"failed"`
	Results []DuplicateMergeResult `json:"results"`
}

func (e *dupEnv) bulk(body string) (int, dupMergeResponse) {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.h.MergeDuplicates(rec, httptest.NewRequest(http.MethodPost, "/api/v1/series/duplicates/merge", strings.NewReader(body)))
	var out dupMergeResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			e.t.Fatal(err)
		}
	}
	return rec.Code, out
}

func TestSeriesDuplicatesHandler_Lists(t *testing.T) {
	e := newDupEnv(t)
	keep := e.series("calibre-series:1", "Wayfarers", 2)
	src := e.series("abs-series:1", "wayfarers.", 1)
	e.series("abs-series:2", "Murderbot", 1)

	rec := httptest.NewRecorder()
	e.h.Duplicates(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series/duplicates", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Groups []db.TitleDuplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Groups) != 1 {
		t.Fatalf("groups = %+v", out.Groups)
	}
	g := out.Groups[0]
	if g.SuggestedKeepID != keep || !g.SameAuthors || len(g.Series) != 2 || g.Series[1].ID != src {
		t.Errorf("group = %+v", g)
	}
	if g.Series[0].BookCount != 2 || g.Series[0].Authors[0] != "Becky Chambers" {
		t.Errorf("keep = %+v", g.Series[0])
	}
}

func TestSeriesDuplicatesMerge_DryRunWritesNothing(t *testing.T) {
	e := newDupEnv(t)
	keep := e.series("calibre-series:1", "Wayfarers", 2)
	src := e.series("abs-series:1", "Wayfarers", 1)

	code, out := e.bulk(fmt.Sprintf(`{"groups":[{"keepId":%d,"sourceIds":[%d]}],"dryRun":true}`, keep, src))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !out.DryRun || out.Merged != 1 || out.Failed != 0 || len(out.Results) != 1 {
		t.Fatalf("response = %+v", out)
	}
	plan := out.Results[0].Plan
	if plan == nil || len(plan.Sources) != 1 || len(plan.Sources[0].Moved) != 1 {
		t.Errorf("plan = %+v, want one book moving", plan)
	}
	if n := e.count(`SELECT COUNT(*) FROM series WHERE id = ?`, src); n != 1 {
		t.Error("dry run deleted the source")
	}
	if n := e.count(`SELECT COUNT(*) FROM series_aliases`); n != 0 {
		t.Errorf("dry run wrote %d aliases", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM series_books WHERE series_id = ?`, keep); n != 2 {
		t.Errorf("dry run moved books: keep has %d", n)
	}
}

// The bulk merge is the single merge: books move, the source's id becomes an
// alias, the source goes, and its Hardcover link passes to the keep.
func TestSeriesDuplicatesMerge_ReusesMergeSemantics(t *testing.T) {
	e := newDupEnv(t)
	keep := e.series("calibre-series:1", "Wayfarers", 2)
	src := e.series("hc-series:55", "Wayfarers", 1)
	if _, err := e.db.Exec(`INSERT INTO series_hardcover_links (series_id, hardcover_series_id) VALUES (?, 'hc-series:55')`, src); err != nil {
		t.Fatal(err)
	}

	code, out := e.bulk(fmt.Sprintf(`{"groups":[{"keepId":%d,"sourceIds":[%d]}]}`, keep, src))
	if code != http.StatusOK || out.Merged != 1 || out.DryRun {
		t.Fatalf("status %d, response %+v", code, out)
	}
	if n := e.count(`SELECT COUNT(*) FROM series WHERE id = ?`, src); n != 0 {
		t.Error("merge left the source")
	}
	if n := e.count(`SELECT COUNT(*) FROM series_books WHERE series_id = ?`, keep); n != 3 {
		t.Errorf("keep has %d books, want 3", n)
	}
	if s, _ := e.repo.GetByForeignID(e.ctx, "hc-series:55"); s == nil || s.ID != keep {
		t.Errorf("source id resolves to %+v, want the keep (alias)", s)
	}
	if n := e.count(`SELECT COUNT(*) FROM series_hardcover_links WHERE series_id = ?`, keep); n != 1 {
		t.Error("keep did not inherit the source's Hardcover link")
	}
}

func TestSeriesDuplicatesMerge_RejectsGroups(t *testing.T) {
	e := newDupEnv(t)
	keep := e.series("calibre-series:1", "Wayfarers", 1)
	src := e.series("abs-series:1", "Wayfarers", 1)
	other := e.series("abs-series:2", "Murderbot", 1)

	cases := []struct {
		name, body, want string
	}{
		{"different title", fmt.Sprintf(`{"groups":[{"keepId":%d,"sourceIds":[%d]}]}`, keep, other), "is not a duplicate"},
		{"missing keep", fmt.Sprintf(`{"groups":[{"keepId":9999,"sourceIds":[%d]}]}`, src), "does not exist"},
		{"missing source", fmt.Sprintf(`{"groups":[{"keepId":%d,"sourceIds":[9999]}]}`, keep), "does not exist"},
		{"no sources", fmt.Sprintf(`{"groups":[{"keepId":%d,"sourceIds":[]}]}`, keep), "no series"},
		{"self", fmt.Sprintf(`{"groups":[{"keepId":%d,"sourceIds":[%d]}]}`, keep, keep), "itself"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := e.bulk(tc.body)
			if code != http.StatusOK || out.Failed != 1 || out.Merged != 0 {
				t.Fatalf("status %d, response %+v", code, out)
			}
			if r := out.Results[0]; r.OK || !strings.Contains(r.Error, tc.want) {
				t.Errorf("result = %+v, want error containing %q", r, tc.want)
			}
		})
	}
	if n := e.count(`SELECT COUNT(*) FROM series`); n != 3 {
		t.Errorf("series = %d, want all three left", n)
	}

	for _, body := range []string{`{`, `{"groups":[]}`} {
		if code, _ := e.bulk(body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, code)
		}
	}
}

// One group failing, at validation or at write, leaves that group whole and
// does not stop the others; the results say which was which.
func TestSeriesDuplicatesMerge_PartialFailure(t *testing.T) {
	e := newDupEnv(t)
	k1 := e.series("calibre-series:1", "Wayfarers", 1)
	s1 := e.series("abs-series:1", "Wayfarers", 1)
	k2 := e.series("calibre-series:2", "Murderbot", 1)
	s2 := e.series("abs-series:2", "Murderbot", 2)
	k3 := e.series("calibre-series:3", "Dune", 1)
	s3 := e.series("abs-series:3", "Dune", 1)
	// The second group's source cannot be deleted, so its merge fails after
	// moving its books; the transaction must undo the move.
	if _, err := e.db.Exec(fmt.Sprintf(`CREATE TRIGGER no_delete_s2 BEFORE DELETE ON series WHEN OLD.id = %d
		BEGIN SELECT RAISE(ABORT, 'boom'); END`, s2)); err != nil {
		t.Fatal(err)
	}

	code, out := e.bulk(fmt.Sprintf(`{"groups":[{"keepId":%d,"sourceIds":[%d]},{"keepId":%d,"sourceIds":[%d]},{"keepId":%d,"sourceIds":[%d]},{"keepId":%d,"sourceIds":[%d]}]}`,
		k1, s1, k2, s2, k3, s3, k3, s1))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if out.Merged != 2 || out.Failed != 2 || len(out.Results) != 4 {
		t.Fatalf("response = %+v", out)
	}
	if !out.Results[0].OK || out.Results[1].OK || !out.Results[2].OK || out.Results[3].OK {
		t.Errorf("results = %+v", out.Results)
	}
	if !strings.Contains(out.Results[3].Error, "another group") {
		t.Errorf("reused id error = %q", out.Results[3].Error)
	}
	if n := e.count(`SELECT COUNT(*) FROM series_books WHERE series_id = ?`, k2); n != 1 {
		t.Errorf("failed group's keep has %d books, want its own 1 (half merged)", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM series_books WHERE series_id = ?`, s2); n != 2 {
		t.Errorf("failed group's source has %d books, want 2", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM series_aliases WHERE series_id = ?`, k2); n != 0 {
		t.Errorf("failed group wrote %d aliases", n)
	}
	for _, id := range []int64{s1, s3} {
		if n := e.count(`SELECT COUNT(*) FROM series WHERE id = ?`, id); n != 0 {
			t.Errorf("series %d should have been merged away", id)
		}
	}
}
