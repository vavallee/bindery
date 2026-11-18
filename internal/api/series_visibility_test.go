package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type seriesVisFixture struct {
	h                  *SeriesHandler
	alice, bob, admin  int64
	aliceOnly, bobOnly int64 // series linked only to that user's book
	shared, mixed      int64 // series with an unowned book; series with both users' books
	empty              int64 // series with no books
	aliceBook, bobBook int64
}

func newSeriesVisFixture(t *testing.T, tenancy bool) *seriesVisFixture {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, tenancy)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	users := db.NewUserRepo(database)
	mk := func(name string) int64 {
		u, err := users.Create(ctx, name, "x")
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	f := &seriesVisFixture{alice: mk("alice"), bob: mk("bob"), admin: mk("root")}
	series, books, authors := db.NewSeriesRepo(database), db.NewBookRepo(database), db.NewAuthorRepo(database)
	f.h = NewSeriesHandler(series, books, authors, metadata.NewAggregator(&stubSeriesProvider{}).WithAudnexClient(nil), nil)

	author := &models.Author{ForeignID: "OL-VIS-A", Name: "Vis Author", SortName: "Author, Vis"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := func(key string, owner int64) int64 {
		b := &models.Book{ForeignID: "OL-VIS-" + key, AuthorID: author.ID, Title: "Book " + key, SortTitle: "Book " + key,
			Status: models.BookStatusWanted, Genres: []string{}, OwnerUserID: owner}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	mkSeries := func(key string, bookIDs ...int64) int64 {
		s := &models.Series{ForeignID: "S-" + key, Title: key + " Saga"}
		if err := series.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		for i, id := range bookIDs {
			if err := series.LinkBook(ctx, s.ID, id, strconv.Itoa(i+1), i == 0); err != nil {
				t.Fatal(err)
			}
		}
		// A Hardcover link so the link route has something to show.
		if err := series.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: s.ID, HardcoverSeriesID: "hc-series:" + key,
			HardcoverProviderID: key, HardcoverTitle: key + " HC", Confidence: 1, LinkedBy: "manual"}); err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	f.aliceBook, f.bobBook = book("a", f.alice), book("b", f.bob)
	unowned := book("u", 0)
	f.aliceOnly = mkSeries("Alice", f.aliceBook)
	f.bobOnly = mkSeries("Bob", f.bobBook)
	f.shared = mkSeries("Shared", unowned)
	f.mixed = mkSeries("Mixed", f.aliceBook, f.bobBook)
	s := &models.Series{ForeignID: "S-empty", Title: "Empty Saga"}
	if err := series.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	f.empty = s.ID
	return f
}

func (f *seriesVisFixture) ctx(uid int64, admin bool) context.Context {
	c := auth.WithUserID(context.Background(), uid)
	if admin {
		c = auth.WithUserRole(c, "admin")
	}
	return c
}

func (f *seriesVisFixture) do(h http.HandlerFunc, ctx context.Context, target string, id int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	if id != 0 {
		req = withURLParam(req, "id", strconv.FormatInt(id, 10))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func visListOf(t *testing.T, body []byte, paged bool) ([]models.Series, int) {
	t.Helper()
	if !paged {
		var l []models.Series
		if err := json.Unmarshal(body, &l); err != nil {
			t.Fatal(err)
		}
		return l, len(l)
	}
	var env seriesListResponse
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	return env.Items, env.Total
}

func idSet(l []models.Series) map[int64]models.Series {
	m := map[int64]models.Series{}
	for _, s := range l {
		m[s.ID] = s
	}
	return m
}

func TestSeriesVisibility_ListAndGetPerUser(t *testing.T) {
	f := newSeriesVisFixture(t, true)
	bob := f.ctx(f.bob, false)

	for _, paged := range []bool{false, true} {
		target := "/api/v1/series"
		if paged {
			target += "?limit=50"
		}
		rec := f.do(f.h.List, bob, target, 0)
		if rec.Code != http.StatusOK {
			t.Fatalf("paged=%v list status %d", paged, rec.Code)
		}
		list, total := visListOf(t, rec.Body.Bytes(), paged)
		got := idSet(list)
		if _, ok := got[f.aliceOnly]; ok {
			t.Errorf("paged=%v: bob's list shows alice's series", paged)
		}
		if _, ok := got[f.empty]; ok {
			t.Errorf("paged=%v: bob's list shows a series with no books", paged)
		}
		for name, id := range map[string]int64{"own": f.bobOnly, "shared": f.shared, "mixed": f.mixed} {
			if _, ok := got[id]; !ok {
				t.Errorf("paged=%v: bob's list is missing the %s series", paged, name)
			}
		}
		if len(list) != 3 || total != 3 {
			t.Errorf("paged=%v: got %d rows and total %d, want 3 and 3", paged, len(list), total)
		}
		// The mixed series shows bob only his book and no alice book.
		for _, sb := range got[f.mixed].Books {
			if sb.BookID == f.aliceBook {
				t.Errorf("paged=%v: bob sees alice's book inside the mixed series", paged)
			}
		}
		if n := len(got[f.mixed].Books); n != 1 {
			t.Errorf("paged=%v: bob sees %d books in the mixed series, want 1", paged, n)
		}
	}

	// By id: hidden is 404, visible is 200 and carries only bob's books.
	for name, id := range map[string]int64{"alice only": f.aliceOnly, "no books": f.empty, "missing": 9999} {
		if rec := f.do(f.h.Get, bob, "/api/v1/series/x", id); rec.Code != http.StatusNotFound {
			t.Errorf("bob GET %s = %d, want 404 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	for name, id := range map[string]int64{"own": f.bobOnly, "shared": f.shared, "mixed": f.mixed} {
		if rec := f.do(f.h.Get, bob, "/api/v1/series/x", id); rec.Code != http.StatusOK {
			t.Errorf("bob GET %s = %d, want 200", name, rec.Code)
		}
	}
	var mixed models.Series
	rec := f.do(f.h.Get, bob, "/api/v1/series/x", f.mixed)
	if err := json.Unmarshal(rec.Body.Bytes(), &mixed); err != nil {
		t.Fatal(err)
	}
	if len(mixed.Books) != 1 || mixed.Books[0].BookID != f.bobBook {
		t.Errorf("bob's mixed series books = %+v, want only his own", mixed.Books)
	}
	// Alice sees the same mixed series with her own book.
	rec = f.do(f.h.Get, f.ctx(f.alice, false), "/api/v1/series/x", f.mixed)
	var am models.Series
	_ = json.Unmarshal(rec.Body.Bytes(), &am)
	if rec.Code != http.StatusOK || len(am.Books) != 1 || am.Books[0].BookID != f.aliceBook {
		t.Errorf("alice mixed = %d %+v", rec.Code, am.Books)
	}
}

func TestSeriesVisibility_HardcoverRoutesAnswer404ForHiddenSeries(t *testing.T) {
	f := newSeriesVisFixture(t, true)
	bob := f.ctx(f.bob, false)
	for name, h := range map[string]http.HandlerFunc{"hardcover-link": f.h.GetHardcoverLink, "hardcover-diff": f.h.HardcoverDiff} {
		rec := f.do(h, bob, "/api/v1/series/x/"+name, f.aliceOnly)
		if rec.Code != http.StatusNotFound || rec.Body.String() == "" {
			t.Errorf("%s on alice's series = %d, want 404", name, rec.Code)
		}
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["error"] != "series not found" {
			t.Errorf("%s hidden answer = %q, want the same body a missing series gets", name, body["error"])
		}
	}
	// Bob's own series still returns its link.
	if rec := f.do(f.h.GetHardcoverLink, bob, "/api/v1/series/x/hardcover-link", f.bobOnly); rec.Code != http.StatusOK {
		t.Errorf("bob's own hardcover link = %d, want 200", rec.Code)
	}
}

func TestSeriesVisibility_AdminAndTenancyOffSeeEverything(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
		admin   bool
	}{{"admin", true, true}, {"tenancy off", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeriesVisFixture(t, tc.tenancy)
			ctx := f.ctx(f.bob, tc.admin)
			for _, paged := range []bool{false, true} {
				target := "/api/v1/series"
				if paged {
					target += "?limit=50"
				}
				list, total := visListOf(t, f.do(f.h.List, ctx, target, 0).Body.Bytes(), paged)
				if len(list) != 5 || total != 5 {
					t.Errorf("paged=%v: %d rows, total %d, want 5 and 5", paged, len(list), total)
				}
			}
			for _, id := range []int64{f.aliceOnly, f.bobOnly, f.shared, f.mixed, f.empty} {
				if rec := f.do(f.h.Get, ctx, "/api/v1/series/x", id); rec.Code != http.StatusOK {
					t.Errorf("GET %d = %d, want 200", id, rec.Code)
				}
			}
		})
	}
}

func TestSeriesVisibility_PagedTotalMatchesFilteredList(t *testing.T) {
	f := newSeriesVisFixture(t, true)
	rec := f.do(f.h.List, f.ctx(f.bob, false), "/api/v1/series?limit=2&offset=0", 0)
	list, total := visListOf(t, rec.Body.Bytes(), true)
	if total != 3 || len(list) != 2 {
		t.Fatalf("page 1: %d rows, total %d, want 2 and 3", len(list), total)
	}
	rec = f.do(f.h.List, f.ctx(f.bob, false), "/api/v1/series?limit=2&offset=2", 0)
	list, total = visListOf(t, rec.Body.Bytes(), true)
	if total != 3 || len(list) != 1 {
		t.Fatalf("page 2: %d rows, total %d, want 1 and 3", len(list), total)
	}
}
