package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// With tenancy on, a shared author (no owner) can hold books from several
// accounts' libraries. These tests pin that the read routes and the author
// level cascades only reach the caller's own books and unowned ones, and that
// admins and tenancy off keep seeing and changing everything.

type scopeFixture struct {
	*covAuEnv
	shared                        *models.Author // unowned author holding alice's and bob's books
	aliceShared, bobShared        *models.Book
	aliceSharedX, bobSharedX      *models.Book // excluded
	aliceDup1, aliceDup2, bobDup1 *models.Book // same title "Emma" under shared
	bobPer1, bobPer2              *models.Book // "Persuasion" pair, bob only
	aliceOwn, bobOwn              *models.Book // under each one's own author
}

func (f *scopeFixture) book(t *testing.T, authorID int64, fid, title string, owner int64, excluded bool) *models.Book {
	t.Helper()
	b := &models.Book{ForeignID: fid, AuthorID: authorID, Title: title, SortTitle: title,
		Status: models.BookStatusWanted, Genres: []string{}, MediaType: models.MediaTypeEbook,
		MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: owner}
	if err := f.books.Create(f.ctx, b); err != nil {
		t.Fatal(err)
	}
	if excluded {
		if err := f.books.SetExcluded(f.ctx, b.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.books.GetByID(f.ctx, b.ID)
	if err != nil || got == nil {
		t.Fatalf("reload book %d: %v", b.ID, err)
	}
	return got
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	f := &scopeFixture{covAuEnv: covAuSetup(t)}
	f.shared = &models.Author{ForeignID: "OL-SHARED-S", Name: "Shared Scope", SortName: "Scope, Shared", Monitored: true, MetadataProvider: "openlibrary"}
	if err := f.authors.Create(f.ctx, f.shared); err != nil {
		t.Fatal(err)
	}
	f.aliceShared = f.book(t, f.shared.ID, "S-A1", "Alice Shared", f.alice, false)
	f.bobShared = f.book(t, f.shared.ID, "S-B1", "Bob Shared", f.bob, false)
	f.aliceSharedX = f.book(t, f.shared.ID, "S-A2", "Alice Hidden", f.alice, true)
	f.bobSharedX = f.book(t, f.shared.ID, "S-B2", "Bob Hidden", f.bob, true)
	f.aliceDup1 = f.book(t, f.shared.ID, "S-E1", "Emma", f.alice, false)
	f.aliceDup2 = f.book(t, f.shared.ID, "S-E2", "Emma", f.alice, false)
	f.bobDup1 = f.book(t, f.shared.ID, "S-E3", "Emma", f.bob, false)
	f.bobPer1 = f.book(t, f.shared.ID, "S-P1", "Persuasion", f.bob, false)
	f.bobPer2 = f.book(t, f.shared.ID, "S-P2", "Persuasion", f.bob, false)
	f.aliceOwn = f.book(t, f.aliceA.ID, "A-1", "Alice Own", f.alice, false)
	f.bobOwn = f.book(t, f.bobA.ID, "B-1", "Bob Own", f.bob, false)
	return f
}

func scopeIDs(books ...*models.Book) []int64 {
	out := make([]int64, 0, len(books))
	for _, b := range books {
		out = append(out, b.ID)
	}
	return out
}

func scopeSorted(in []int64) []int64 {
	out := append([]int64(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameScopeIDs(a, b []int64) bool {
	a, b = scopeSorted(a), scopeSorted(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (f *scopeFixture) listBooks(t *testing.T, query string, user int64, role string) []int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	f.bookHandlerForScope().List(rec, covAuRequest(http.MethodGet, "/api/v1/book"+query, "", nil, user, role))
	if rec.Code != http.StatusOK {
		t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body.String())
	}
	var resp bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != len(resp.Items) {
		t.Fatalf("list %s: total %d but %d items", query, resp.Total, len(resp.Items))
	}
	out := []int64{}
	for _, b := range resp.Items {
		out = append(out, b.ID)
	}
	return out
}

func (f *scopeFixture) bookHandlerForScope() *BookHandler {
	return NewBookHandler(f.books, nil, nil, nil)
}

func TestBookList_IncludeExcluded_ScopedToCaller(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newScopeFixture(t)
	sharedQ := fmt.Sprintf("&authorId=%d", f.shared.ID)

	// Alice: her own excluded and active books, never bob's.
	got := f.listBooks(t, "?includeExcluded=true&limit=500", f.alice, "user")
	want := scopeIDs(f.aliceShared, f.aliceSharedX, f.aliceDup1, f.aliceDup2, f.aliceOwn)
	if !sameScopeIDs(got, want) {
		t.Fatalf("alice includeExcluded all: got %v want %v", scopeSorted(got), scopeSorted(want))
	}
	got = f.listBooks(t, "?includeExcluded=true&limit=500"+sharedQ, f.alice, "user")
	want = scopeIDs(f.aliceShared, f.aliceSharedX, f.aliceDup1, f.aliceDup2)
	if !sameScopeIDs(got, want) {
		t.Fatalf("alice includeExcluded by author: got %v want %v", scopeSorted(got), scopeSorted(want))
	}
	got = f.listBooks(t, "?includeExcluded=true&limit=500&status=wanted", f.alice, "user")
	want = scopeIDs(f.aliceShared, f.aliceSharedX, f.aliceDup1, f.aliceDup2, f.aliceOwn)
	if !sameScopeIDs(got, want) {
		t.Fatalf("alice includeExcluded by status: got %v want %v", scopeSorted(got), scopeSorted(want))
	}
	// Alice's own excluded book is still returned (regression guard).
	if !scopeContains(f.listBooks(t, "?includeExcluded=true&limit=500", f.alice, "user"), f.aliceSharedX.ID) {
		t.Fatal("alice's own excluded book disappeared")
	}
	// Bob never sees alice's.
	for _, id := range f.listBooks(t, "?includeExcluded=true&limit=500", f.bob, "user") {
		for _, a := range scopeIDs(f.aliceShared, f.aliceSharedX, f.aliceDup1, f.aliceDup2, f.aliceOwn) {
			if id == a {
				t.Fatalf("bob saw alice's book %d", id)
			}
		}
	}

	// Admin sees everything.
	if n := len(f.listBooks(t, "?includeExcluded=true&limit=500", 99, "admin")); n != 11 {
		t.Fatalf("admin sees %d books, want 11", n)
	}
	if n := len(f.listBooks(t, "?includeExcluded=true&limit=500"+sharedQ, 99, "admin")); n != 9 {
		t.Fatalf("admin by author sees %d books, want 9", n)
	}
	if n := len(f.listBooks(t, "?includeExcluded=true&limit=500&status=wanted", 99, "admin")); n != 11 {
		t.Fatalf("admin by status sees %d books, want 11", n)
	}
}

func TestBookList_IncludeExcluded_TenancyOffSeesEverything(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newScopeFixture(t)
	if n := len(f.listBooks(t, "?includeExcluded=true&limit=500", f.alice, "user")); n != 11 {
		t.Fatalf("tenancy off sees %d books, want 11", n)
	}
}

func scopeContains(in []int64, id int64) bool {
	for _, v := range in {
		if v == id {
			return true
		}
	}
	return false
}

func (f *scopeFixture) authorDuplicates(t *testing.T, authorID, user int64, role string) dupCandResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).DuplicateCandidates(rec, covAuRequest(http.MethodGet, "/", "", idParam(authorID), user, role))
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate-candidates: %d %s", rec.Code, rec.Body.String())
	}
	return parseDupCand(t, rec)
}

func TestAuthorDuplicateCandidates_ScopedToCaller(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newScopeFixture(t)

	resp := f.authorDuplicates(t, f.shared.ID, f.alice, "user")
	if resp.Count != 1 || len(resp.Groups) != 1 {
		t.Fatalf("alice: count %d groups %d, want exactly the Emma group", resp.Count, len(resp.Groups))
	}
	var got []int64
	for _, m := range resp.Groups[0].Books {
		got = append(got, m.ID)
	}
	if !sameScopeIDs(got, scopeIDs(f.aliceDup1, f.aliceDup2)) {
		t.Fatalf("alice's Emma group members %v, want only her two", got)
	}

	// Bob owns the Persuasion pair plus one Emma: his Emma has no partner.
	resp = f.authorDuplicates(t, f.shared.ID, f.bob, "user")
	if resp.Count != 1 || len(resp.Groups) != 1 || resp.Groups[0].Key != "persuasion" {
		t.Fatalf("bob: %+v, want only the Persuasion group", resp)
	}

	// Admin sees both groups, Emma with all three.
	resp = f.authorDuplicates(t, f.shared.ID, 99, "admin")
	if resp.Count != 2 {
		t.Fatalf("admin: count %d, want 2", resp.Count)
	}
	for _, g := range resp.Groups {
		if g.Key == "emma" && len(g.Books) != 3 {
			t.Fatalf("admin Emma members %d, want 3", len(g.Books))
		}
	}
}

func TestAuthorDuplicateCandidates_TenancyOffSeesEverything(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newScopeFixture(t)
	resp := f.authorDuplicates(t, f.shared.ID, f.alice, "user")
	if resp.Count != 2 {
		t.Fatalf("tenancy off: count %d, want 2", resp.Count)
	}
}

func (f *scopeFixture) libraryDuplicates(t *testing.T, user int64, role string) libraryDuplicatesResponse {
	t.Helper()
	h := NewDuplicateReviewHandler(f.books, f.series)
	rec := httptest.NewRecorder()
	h.List(rec, covAuRequest(http.MethodGet, "/api/v1/library/duplicate-candidates", "", nil, user, role))
	if rec.Code != http.StatusOK {
		t.Fatalf("library duplicates: %d %s", rec.Code, rec.Body.String())
	}
	var resp libraryDuplicatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLibraryDuplicateCandidates_ScopedToCaller(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newScopeFixture(t)

	resp := f.libraryDuplicates(t, f.alice, "user")
	if resp.Total != 1 || resp.Count != 1 || len(resp.Groups) != 1 {
		t.Fatalf("alice: total %d count %d groups %d, want only the Emma group", resp.Total, resp.Count, len(resp.Groups))
	}
	var got []int64
	for _, m := range resp.Groups[0].Members {
		got = append(got, m.ID)
	}
	if !sameScopeIDs(got, scopeIDs(f.aliceDup1, f.aliceDup2)) {
		t.Fatalf("alice's members %v, want only her two", got)
	}

	resp = f.libraryDuplicates(t, f.bob, "user")
	if resp.Total != 1 || len(resp.Groups) != 1 || resp.Groups[0].Key != "persuasion" {
		t.Fatalf("bob: %+v, want only Persuasion", resp)
	}

	resp = f.libraryDuplicates(t, 99, "admin")
	if resp.Total != 2 {
		t.Fatalf("admin total %d, want 2", resp.Total)
	}
}

func TestLibraryDuplicateCandidates_TenancyOffSeesEverything(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newScopeFixture(t)
	if resp := f.libraryDuplicates(t, f.alice, "user"); resp.Total != 2 {
		t.Fatalf("tenancy off total %d, want 2", resp.Total)
	}
}

func TestAuthorSeries_ScopedToCaller(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newScopeFixture(t)
	aliceSeries, err := f.series.CreateManual(f.ctx, "Alice Saga")
	if err != nil {
		t.Fatal(err)
	}
	bobSeries, err := f.series.CreateManual(f.ctx, "Bob Saga")
	if err != nil {
		t.Fatal(err)
	}
	mixed, err := f.series.CreateManual(f.ctx, "Mixed Saga")
	if err != nil {
		t.Fatal(err)
	}
	link := func(s int64, b *models.Book, pos string) {
		if err := f.series.UpsertBookLink(f.ctx, s, b.ID, pos, true); err != nil {
			t.Fatal(err)
		}
	}
	link(aliceSeries.ID, f.aliceShared, "1")
	link(bobSeries.ID, f.bobShared, "1")
	link(mixed.ID, f.aliceDup1, "1")
	link(mixed.ID, f.bobDup1, "2")

	list := func(user int64, role string) map[string][]int64 {
		rec := httptest.NewRecorder()
		covAuHandler(f.covAuEnv, nil).ListSeries(rec, covAuRequest(http.MethodGet, "/", "", idParam(f.shared.ID), user, role))
		if rec.Code != http.StatusOK {
			t.Fatalf("series: %d %s", rec.Code, rec.Body.String())
		}
		var out []models.Series
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		m := map[string][]int64{}
		for _, s := range out {
			m[s.Title] = []int64{}
			for _, sb := range s.Books {
				m[s.Title] = append(m[s.Title], sb.BookID)
			}
		}
		return m
	}

	got := list(f.alice, "user")
	if _, leaked := got["Bob Saga"]; leaked {
		t.Fatalf("alice sees bob's series: %v", got)
	}
	if !sameScopeIDs(got["Mixed Saga"], scopeIDs(f.aliceDup1)) || !sameScopeIDs(got["Alice Saga"], scopeIDs(f.aliceShared)) || len(got) != 2 {
		t.Fatalf("alice series = %v", got)
	}
	admin := list(99, "admin")
	if len(admin) != 3 || len(admin["Mixed Saga"]) != 2 {
		t.Fatalf("admin series = %v", admin)
	}
	auth.SetEnforceTenancyForTests(t, false)
	off := list(f.alice, "user")
	if len(off) != 3 || len(off["Mixed Saga"]) != 2 {
		t.Fatalf("tenancy off series = %v", off)
	}
}

// writeFixture: alice owns the author, which also holds bob's book and an
// unowned book. Cascades from alice must only touch her book.
type writeFixture struct {
	*scopeFixture
	author             *models.Author
	mine, bobs, shared *models.Book
}

func newWriteFixture(t *testing.T) *writeFixture {
	t.Helper()
	s := newScopeFixture(t)
	w := &writeFixture{scopeFixture: s, author: s.aliceA}
	w.mine = s.aliceOwn
	w.bobs = s.book(t, s.aliceA.ID, "W-B1", "Bob In Alice Author", s.bob, false)
	w.shared = s.book(t, s.aliceA.ID, "W-S1", "Unowned In Alice Author", 0, false)
	return w
}

func (w *writeFixture) reload(t *testing.T, b *models.Book) *models.Book {
	t.Helper()
	got, err := w.books.GetByID(w.ctx, b.ID)
	if err != nil || got == nil {
		t.Fatalf("reload %d: %v", b.ID, err)
	}
	return got
}

func TestAuthorUpdate_ApplyMonitorMode_OnlyCallersBooks(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tenancy       bool
		user          int64
		role          string
		wantBobsMon   bool
		wantSharedMon bool
	}{
		{"tenancy on, alice", true, 0, "user", true, true},
		{"tenancy on, admin", true, 99, "admin", false, false},
		{"tenancy off", false, 0, "user", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			w := newWriteFixture(t)
			user := tc.user
			if user == 0 {
				user = w.alice
			}
			rec := httptest.NewRecorder()
			covAuHandler(w.covAuEnv, nil).Update(rec, covAuRequest(http.MethodPut, "/",
				`{"monitorMode":"none","applyMonitorModeToExisting":true}`, idParam(w.author.ID), user, tc.role))
			if rec.Code != http.StatusOK {
				t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
			}
			if w.reload(t, w.mine).Monitored {
				t.Error("the caller's own book should have been unmonitored")
			}
			if got := w.reload(t, w.bobs).Monitored; got != tc.wantBobsMon {
				t.Errorf("bob's book monitored=%v, want %v", got, tc.wantBobsMon)
			}
			if got := w.reload(t, w.shared).Monitored; got != tc.wantSharedMon {
				t.Errorf("unowned book monitored=%v, want %v", got, tc.wantSharedMon)
			}
		})
	}
}

func TestAuthorApplyGenres_OnlyCallersBooks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
		admin   bool
		wantAll bool
	}{
		{"tenancy on, alice", true, false, false},
		{"tenancy on, admin", true, true, true},
		{"tenancy off", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			w := newWriteFixture(t)
			user, role := w.alice, "user"
			if tc.admin {
				user, role = 99, "admin"
			}
			rec := httptest.NewRecorder()
			covAuHandler(w.covAuEnv, nil).ApplyGenres(rec, covAuRequest(http.MethodPut, "/", `{"genres":["Fantasy"]}`, idParam(w.author.ID), user, role))
			if rec.Code != http.StatusOK {
				t.Fatalf("genres: %d %s", rec.Code, rec.Body.String())
			}
			if g := w.reload(t, w.mine).Genres; len(g) != 1 || g[0] != "Fantasy" {
				t.Errorf("caller's own book genres = %v", g)
			}
			for name, b := range map[string]*models.Book{"bob's": w.bobs, "unowned": w.shared} {
				changed := len(w.reload(t, b).Genres) == 1
				if changed != tc.wantAll {
					t.Errorf("%s book changed=%v, want %v", name, changed, tc.wantAll)
				}
			}
		})
	}
}

func TestBulkAuthorActions_OnlyCallersBooks(t *testing.T) {
	for _, action := range []string{"set_media_type", "monitor", "set_monitor_mode"} {
		for _, tc := range []struct {
			name    string
			tenancy bool
			admin   bool
			wantAll bool
		}{
			{"tenancy on", true, false, false},
			{"admin", true, true, true},
			{"tenancy off", false, false, true},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				auth.SetEnforceTenancyForTests(t, tc.tenancy)
				w := newWriteFixture(t)
				// Start every book monitored and ebook; each action moves one flag.
				user, role := w.alice, "user"
				if tc.admin {
					user, role = 99, "admin"
				}
				var body string
				switch action {
				case "set_media_type":
					body = fmt.Sprintf(`{"ids":[%d],"action":"set_media_type","mediaType":"audiobook"}`, w.author.ID)
				case "monitor":
					body = fmt.Sprintf(`{"ids":[%d],"action":"monitor","applyMonitorModeToExisting":true}`, w.author.ID)
				case "set_monitor_mode":
					body = fmt.Sprintf(`{"ids":[%d],"action":"set_monitor_mode","monitorMode":"none","applyMonitorModeToExisting":true}`, w.author.ID)
				}
				if action != "set_media_type" {
					// monitor with the default mode "all" would monitor
					// everything already; make the author "none" so the
					// cascade has something to flip for books that start monitored.
					w.author.MonitorMode = models.AuthorMonitorModeNone
					if err := w.authors.Update(w.ctx, w.author); err != nil {
						t.Fatal(err)
					}
				}
				h := NewBulkHandler(w.authors, w.books, nil, nil).WithSeriesRepo(w.series)
				rec := postBulkAs(t, h.AuthorsBulk, body, user, role)
				if rec.Code != http.StatusOK {
					t.Fatalf("bulk: %d %s", rec.Code, rec.Body.String())
				}
				if !resultOK(t, rec, w.author.ID) {
					t.Fatalf("bulk result not ok: %s", rec.Body.String())
				}
				changed := func(b *models.Book) bool {
					got := w.reload(t, b)
					if action == "set_media_type" {
						return got.MediaType == models.MediaTypeAudiobook
					}
					return !got.Monitored
				}
				if !changed(w.mine) {
					t.Error("the caller's own book was not changed")
				}
				for name, b := range map[string]*models.Book{"bob's": w.bobs, "unowned": w.shared} {
					if got := changed(b); got != tc.wantAll {
						t.Errorf("%s book changed=%v, want %v", name, got, tc.wantAll)
					}
				}
			})
		}
	}
}

func TestBulkAuthorSearch_OnlyCallersBooks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
		admin   bool
		want    []int64
	}{
		{"tenancy on", true, false, nil},
		{"admin", true, true, nil},
		{"tenancy off", false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			searchPaceInterval = 0
			t.Cleanup(func() { searchPaceInterval = 3 * time.Second })
			w := newWriteFixture(t)
			user, role := w.alice, "user"
			if tc.admin {
				user, role = 99, "admin"
			}
			spy := newMockBookSearcher()
			h := NewBulkHandler(w.authors, w.books, nil, spy)
			rec := postBulkAs(t, h.AuthorsBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, w.author.ID), user, role)
			if rec.Code != http.StatusOK {
				t.Fatalf("bulk search: %d %s", rec.Code, rec.Body.String())
			}
			want := 3
			if tc.tenancy && !tc.admin {
				want = 1
			}
			seen := map[int64]bool{}
			for i := 0; i < want; i++ {
				seen[spy.waitForCall(t, 2*time.Second).ID] = true
			}
			spy.assertNoCall(t, 300*time.Millisecond)
			if !seen[w.mine.ID] {
				t.Error("the caller's own book was not searched")
			}
			if (seen[w.bobs.ID] || seen[w.shared.ID]) != (want == 3) {
				t.Errorf("searched set %v does not match scope (want %d searches)", seen, want)
			}
		})
	}
}
