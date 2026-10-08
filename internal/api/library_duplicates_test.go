package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type libDupResponse struct {
	Groups []dupEvidenceGroup `json:"groups"`
	Total  int                `json:"total"`
	Count  int                `json:"count"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

type libDupFixture struct {
	*dupCandFixture
	lib *DuplicateReviewHandler
}

func newLibDupFixture(t *testing.T) *libDupFixture {
	t.Helper()
	f, _ := newDupCandFixture(t)
	return &libDupFixture{dupCandFixture: f, lib: NewDuplicateReviewHandler(f.books, f.series)}
}

func (f *libDupFixture) author(t *testing.T, name string, ownerID int64) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: "OLX-" + name, Name: name, SortName: name, Monitored: true, MetadataProvider: "openlibrary"}
	var err error
	if ownerID != 0 {
		err = f.authors.CreateForUser(f.ctx, a, ownerID)
	} else {
		err = f.authors.Create(f.ctx, a)
	}
	if err != nil {
		t.Fatalf("create author %q: %v", name, err)
	}
	return a
}

// list calls the handler. role "" leaves the context without a role, the
// way a context built outside the auth middleware looks.
func (f *libDupFixture) list(t *testing.T, query string, userID int64, role string) libDupResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/library/duplicate-candidates"+query, nil)
	ctx := req.Context()
	if userID != 0 {
		ctx = auth.WithUserID(ctx, userID)
	}
	if role != "" {
		ctx = auth.WithUserRole(ctx, role)
	}
	rec := httptest.NewRecorder()
	f.lib.List(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp libDupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return resp
}

func groupAuthors(resp libDupResponse) []string {
	out := make([]string, 0, len(resp.Groups))
	for _, g := range resp.Groups {
		out = append(out, g.AuthorName+"/"+g.Key)
	}
	return out
}

// TestLibraryDuplicates_PaginatesAcrossAuthors: groups from every author,
// ordered by author name then key, cut into pages with a stable total, and a
// title shared by two different authors is never a group.
func TestLibraryDuplicates_PaginatesAcrossAuthors(t *testing.T) {
	f := newLibDupFixture(t)
	weir := f.author(t, "Andy Weir", 0)
	herbert := f.author(t, "Frank Herbert", 0)
	hannah := f.author(t, "kristin Hannah", 0) // lower case on purpose: ordering ignores case
	lone := f.author(t, "Lone Author", 0)

	f.addBook(t, herbert.ID, "Dune", "OL1W", false)
	f.addBook(t, herbert.ID, "Dune", "OL2W", false)
	f.addBook(t, herbert.ID, "Children of Dune", "OL3W", false)
	f.addBook(t, herbert.ID, "Children of Dune", "OL4W", false)
	f.addBook(t, weir.ID, "The Martian", "OL5W", false)
	f.addBook(t, weir.ID, "Martian", "OL6W", false)
	f.addBook(t, hannah.ID, "Nightingale", "OL7W", false)
	f.addBook(t, hannah.ID, "The Nightingale", "OL8W", false)
	// Same title as Herbert's, different author: not a duplicate of his.
	f.addBook(t, lone.ID, "Dune", "OL9W", false)

	first := f.list(t, "?limit=2", 0, "")
	if first.Total != 4 || first.Count != 2 || first.Limit != 2 || first.Offset != 0 {
		t.Fatalf("page 1: total=%d count=%d limit=%d offset=%d", first.Total, first.Count, first.Limit, first.Offset)
	}
	second := f.list(t, "?limit=2&offset=2", 0, "")
	if second.Total != 4 || second.Count != 2 {
		t.Fatalf("page 2: total=%d count=%d", second.Total, second.Count)
	}
	got := append(groupAuthors(first), groupAuthors(second)...)
	want := []string{"Andy Weir/themartian", "Frank Herbert/childrenofdune", "Frank Herbert/dune", "kristin Hannah/nightingale"}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("groups = %v, want %v", got, want)
		}
	}
	for _, g := range first.Groups {
		if g.AuthorName == "Frank Herbert" && g.Key == "dune" && len(g.Books) != 2 {
			t.Errorf("Herbert's Dune group has %d rows; Lone Author's Dune must not join it", len(g.Books))
		}
	}

	past := f.list(t, "?limit=2&offset=10", 0, "")
	if past.Total != 4 || past.Count != 0 || len(past.Groups) != 0 {
		t.Errorf("past the end: total=%d count=%d groups=%d", past.Total, past.Count, len(past.Groups))
	}
	capped := f.list(t, "?limit=100000", 0, "")
	if capped.Limit != libraryDuplicatesMaxLimit {
		t.Errorf("limit = %d, want capped at %d", capped.Limit, libraryDuplicatesMaxLimit)
	}
}

// TestLibraryDuplicates_EvidenceAndExclude: the page carries full rows and
// the same evidence as the per-author window, and excluding the empty row
// through the existing exclude path drops the group on the next load.
func TestLibraryDuplicates_EvidenceAndExclude(t *testing.T) {
	f := newLibDupFixture(t)
	hannah := f.author(t, "Kristin Hannah", 0)
	empty := f.addBook(t, hannah.ID, "Nightingale", "OL7001W", false)
	owned := f.addBook(t, hannah.ID, "The Nightingale", "OL7002W", false)
	if _, err := f.database.ExecContext(f.ctx, `UPDATE books SET description = 'a long blurb', language = 'en' WHERE id IN (?, ?)`, empty.ID, owned.ID); err != nil {
		t.Fatal(err)
	}
	f.addFile(t, owned.ID, "audiobook", "The Nightingale.m4b")

	resp := f.list(t, "", 0, "")
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	g := resp.Groups[0]
	if g.KeeperID != owned.ID || len(g.SuggestedExcludeIDs) != 1 || g.SuggestedExcludeIDs[0] != empty.ID {
		t.Fatalf("keeper = %d suggested = %v", g.KeeperID, g.SuggestedExcludeIDs)
	}
	for _, b := range g.Books {
		if b.Status == "" {
			t.Errorf("row %d not hydrated: empty status", b.ID)
		}
		if b.Description != "" {
			t.Errorf("row %d carries its description; the page should not", b.ID)
		}
		if b.ID == owned.ID && (!b.HasFiles || b.Evidence.Files[0].Format != "m4b" || b.Evidence.Files[0].Kind != "audiobook") {
			t.Errorf("owned evidence = %+v", b.Evidence)
		}
	}

	// The UI's one confirmed click is POST /book/bulk {action: exclude}; the
	// repo call is what that action runs.
	if err := f.books.SetExcluded(f.ctx, empty.ID, true); err != nil {
		t.Fatal(err)
	}
	after := f.list(t, "", 0, "")
	if after.Total != 0 || len(after.Groups) != 0 {
		t.Errorf("after excluding the empty row: total = %d, want 0", after.Total)
	}
}

// TestLibraryDuplicates_Scoping: with tenancy on a non-admin sees only
// groups under authors they own or that are unowned, exactly the authors the
// per-author window would open for them; an admin and a tenancy-off install
// see every group. The route is not admin gated, so the user gets a 200.
func TestLibraryDuplicates_Scoping(t *testing.T) {
	f := newLibDupFixture(t)
	users := db.NewUserRepo(f.database)
	alice, err := users.Create(f.ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(f.ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}
	aliceAuthor := f.author(t, "Alice Author", alice.ID)
	bobAuthor := f.author(t, "Bob Author", bob.ID)
	shared := f.author(t, "Shared Author", 0)
	for i, a := range []*models.Author{aliceAuthor, bobAuthor, shared} {
		f.addBook(t, a.ID, "Dune", "OLA"+strconv.Itoa(i)+"W", false)
		f.addBook(t, a.ID, "Dune", "OLB"+strconv.Itoa(i)+"W", false)
	}

	t.Run("tenancy on, user", func(t *testing.T) {
		auth.SetEnforceTenancyForTests(t, true)
		resp := f.list(t, "", alice.ID, auth.RoleUser)
		got := groupAuthors(resp)
		if resp.Total != 2 || len(got) != 2 || got[0] != "Alice Author/dune" || got[1] != "Shared Author/dune" {
			t.Errorf("alice sees %v (total %d), want her own and the unowned author only", got, resp.Total)
		}
		// And the per-author window agrees: bob's author is a 404 for alice.
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := auth.WithUserRole(auth.WithUserID(req.Context(), alice.ID), auth.RoleUser)
		if auth.CheckOwnership(ctx, bobAuthor.OwnerUserID) {
			t.Error("per-author ownership check would let alice open bob's author")
		}
	})
	t.Run("tenancy on, admin", func(t *testing.T) {
		auth.SetEnforceTenancyForTests(t, true)
		if resp := f.list(t, "", alice.ID, auth.RoleAdmin); resp.Total != 3 {
			t.Errorf("admin total = %d, want 3", resp.Total)
		}
	})
	t.Run("tenancy off", func(t *testing.T) {
		auth.SetEnforceTenancyForTests(t, false)
		if resp := f.list(t, "", alice.ID, auth.RoleUser); resp.Total != 3 {
			t.Errorf("tenancy off total = %d, want 3", resp.Total)
		}
	})
}

// TestLibraryDuplicates_Scale is the reporter's shape (#2999): about 4,500
// books and more than 120 groups. A page must come back well inside an
// interactive budget, which it can only do with a fixed number of queries.
func TestLibraryDuplicates_Scale(t *testing.T) {
	f := newLibDupFixture(t)
	tx, err := f.database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for a := 0; a < 300; a++ {
		res, err := tx.Exec(`INSERT INTO authors (foreign_id, name, sort_name, monitored) VALUES (?, ?, ?, 1)`,
			"OLA"+strconv.Itoa(a)+"A", "Author "+strconv.Itoa(a), "Author "+strconv.Itoa(a))
		if err != nil {
			t.Fatal(err)
		}
		authorID, _ := res.LastInsertId()
		titles := 15
		for b := 0; b < titles; b++ {
			n++
			title := "Author " + strconv.Itoa(a) + " Title " + strconv.Itoa(b)
			if b == titles-1 && a < 130 {
				title = "Author " + strconv.Itoa(a) + " Title 0" // one planted duplicate for 130 authors
			}
			if _, err := tx.Exec(`INSERT INTO books (foreign_id, author_id, title, sort_title, status, monitored, metadata_provider, media_type)
				VALUES (?, ?, ?, ?, 'wanted', 1, 'openlibrary', 'ebook')`, "OLB"+strconv.Itoa(n)+"W", authorID, title, title); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	resp := f.list(t, "?limit=100", 0, "")
	elapsed := time.Since(start)
	t.Logf("library page over %d books: %s", n, elapsed)
	if resp.Total != 130 || resp.Count != 100 {
		t.Errorf("total = %d count = %d, want 130 and 100", resp.Total, resp.Count)
	}
	if elapsed > 5*time.Second {
		t.Errorf("library page over %d books took %s", n, elapsed)
	}

	// The next page comes from the cached scan.
	start = time.Now()
	next := f.list(t, "?limit=100&offset=100", 0, "")
	t.Logf("cached next page: %s", time.Since(start))
	if next.Count != 30 || f.lib.scans != 1 {
		t.Errorf("next page count = %d scans = %d, want 30 and 1", next.Count, f.lib.scans)
	}
}

// TestLibraryDuplicates_CachesScanUntilTheLibraryChanges: turning pages
// reuses one scan; an exclusion, a series position change and the TTL each
// force a rescan, and serving a page never mutates the cached list.
func TestLibraryDuplicates_CachesScanUntilTheLibraryChanges(t *testing.T) {
	f := newLibDupFixture(t)
	a := f.author(t, "Frank Herbert", 0)
	d1 := f.addBook(t, a.ID, "Dune", "OLC1W", false)
	f.addBook(t, a.ID, "Dune", "OLC2W", false)
	f.addBook(t, a.ID, "Dune", "OLC3W", false)
	m1 := f.addBook(t, a.ID, "Mistborn", "OLC4W", false)
	m2 := f.addBook(t, a.ID, "Mistborn: The Final Empire", "OLC5W", false)
	f.linkSeries(t, "OLSC", "Mistborn", m1.ID, "1")
	f.linkSeries(t, "OLSC", "Mistborn", m2.ID, "1")

	clock := time.Now()
	f.lib.now = func() time.Time { return clock }
	scans := func() int {
		f.lib.mu.Lock()
		defer f.lib.mu.Unlock()
		return f.lib.scans
	}

	first := f.list(t, "?limit=1", 0, "")
	second := f.list(t, "?limit=1&offset=1", 0, "")
	again := f.list(t, "?limit=1", 0, "")
	if first.Total != 2 || second.Total != 2 {
		t.Fatalf("totals = %d, %d; want 2", first.Total, second.Total)
	}
	if scans() != 1 {
		t.Errorf("three page loads ran %d scans, want 1", scans())
	}
	fb, _ := json.Marshal(first)
	ab, _ := json.Marshal(again)
	if string(fb) != string(ab) {
		t.Errorf("a cached page changed between requests:\n%s\n%s", fb, ab)
	}

	// Excluding a row changes the stamp: the next load rescans.
	if err := f.books.SetExcluded(f.ctx, d1.ID, true); err != nil {
		t.Fatal(err)
	}
	f.list(t, "?limit=1", 0, "")
	if scans() != 2 {
		t.Errorf("after an exclusion: %d scans, want 2", scans())
	}

	// A series position change (no books row touched) changes the stamp too:
	// moving the second Mistborn row to #2 splits the group.
	if _, err := f.database.ExecContext(f.ctx, `UPDATE series_books SET position_in_series = '2' WHERE book_id = ?`, m2.ID); err != nil {
		t.Fatal(err)
	}
	resp := f.list(t, "", 0, "")
	if scans() != 3 {
		t.Errorf("after a series change: %d scans, want 3", scans())
	}
	if resp.Total != 1 {
		t.Errorf("total = %d after Mistborn #2 split off, want 1 (Dune only)", resp.Total)
	}

	// And the timer bounds what a fingerprint might miss.
	clock = clock.Add(duplicateScanTTL + time.Second)
	f.list(t, "", 0, "")
	if scans() != 4 {
		t.Errorf("after the TTL: %d scans, want 4", scans())
	}
}
