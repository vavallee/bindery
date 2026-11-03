package recommender

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type scopeFixture struct {
	books    *db.BookRepo
	authors  *db.AuthorRepo
	series   *db.SeriesRepo
	recs     *db.RecommendationRepo
	settings *db.SettingsRepo
	users    *db.UserRepo
	admin    int64
	alice    int64
	bob      int64
}

// newScopeFixture seeds three libraries:
//   - alice's: a monitored author with a skipped book ("Alice Secret Title"),
//     plus her skipped book two of a series bob has started;
//   - bob's: a monitored author with a skipped book, and book one of that
//     series, which he wants;
//   - shared: a monitored, unowned author with an unowned skipped book and
//     alice's skipped "Alice Under Shared".
func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	f := scopeFixture{
		books:    db.NewBookRepo(database),
		authors:  db.NewAuthorRepo(database),
		series:   db.NewSeriesRepo(database),
		recs:     db.NewRecommendationRepo(database),
		settings: db.NewSettingsRepo(database),
		users:    db.NewUserRepo(database),
	}
	admin, err := f.users.CreateFirstAdmin(ctx, "admin", "x")
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := f.users.Create(ctx, "alice", "x")
	bob, _ := f.users.Create(ctx, "bob", "x")
	f.admin, f.alice, f.bob = admin.ID, alice.ID, bob.ID
	if err := f.settings.Set(ctx, "recommendations.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	author := func(fid, name string, owner int64) *models.Author {
		a := &models.Author{ForeignID: fid, Name: name, SortName: name, Monitored: true, MetadataProvider: "openlibrary", OwnerUserID: owner}
		if err := f.authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		if owner == 0 {
			if _, err := database.Exec("UPDATE authors SET owner_user_id = NULL WHERE id = ?", a.ID); err != nil {
				t.Fatal(err)
			}
		}
		return a
	}
	book := func(a *models.Author, fid, title, status string, owner int64) *models.Book {
		b := &models.Book{ForeignID: fid, AuthorID: a.ID, Title: title, SortTitle: title, Status: status,
			MetadataProvider: "openlibrary", Genres: []string{}, OwnerUserID: owner}
		if err := f.books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if owner == 0 {
			if _, err := database.Exec("UPDATE books SET owner_user_id = NULL WHERE id = ?", b.ID); err != nil {
				t.Fatal(err)
			}
		}
		return b
	}

	aa := author("OL-alice", "Alice Private Author", f.alice)
	book(aa, "OL-alice-1", "Alice Secret Title", models.BookStatusSkipped, f.alice)
	ba := author("OL-bob", "Bob Author", f.bob)
	book(ba, "OL-bob-1", "Bob Skipped Title", models.BookStatusSkipped, f.bob)
	sa := author("OL-shared", "Shared Author", 0)
	book(sa, "OL-shared-1", "Shared Skipped Title", models.BookStatusSkipped, 0)
	// Alice's own book filed under the shared author.
	book(sa, "OL-alice-shared", "Alice Under Shared", models.BookStatusSkipped, f.alice)

	s := &models.Series{ForeignID: "OL-series", Title: "Common Saga"}
	if err := f.series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	one := book(ba, "OL-saga-1", "Common Saga One", models.BookStatusWanted, f.bob)
	two := book(aa, "OL-saga-2", "Alice Saga Two", models.BookStatusSkipped, f.alice)
	if err := f.series.LinkBook(ctx, s.ID, one.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if err := f.series.LinkBook(ctx, s.ID, two.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f scopeFixture) run(t *testing.T, userID int64, hc *fakeWishlistFetcher) map[string]bool {
	t.Helper()
	ctx := context.Background()
	e := New(f.books, f.authors, f.series, f.recs, f.settings).WithUsers(f.users).WithHCClient(hc)
	if err := e.Run(ctx, userID); err != nil {
		t.Fatal(err)
	}
	got, err := f.recs.List(ctx, userID, "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	for _, r := range got {
		titles[r.Title] = true
		titles["author:"+r.AuthorName] = true
	}
	return titles
}

// Under tenancy a plain user's recommendations come from their own and shared
// books only: another user's author, books and series entries never appear,
// and the instance Hardcover wishlist is not consulted.
func TestRun_TenancyUserSeesOnlyOwnAndSharedLibrary(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newScopeFixture(t)
	hc := &fakeWishlistFetcher{books: []models.RecommendationCandidate{{ForeignID: "hc:wish", Title: "Admin Wishlist Pick", RatingsCount: 500, Rating: 4.5}}}

	got := f.run(t, f.bob, hc)
	for _, leaked := range []string{"Alice Secret Title", "Alice Saga Two", "Alice Under Shared", "author:Alice Private Author", "Admin Wishlist Pick"} {
		if got[leaked] {
			t.Errorf("bob's recommendations include %q: %v", leaked, got)
		}
	}
	for _, want := range []string{"Bob Skipped Title", "Shared Skipped Title"} {
		if !got[want] {
			t.Errorf("bob's recommendations miss %q: %v", want, got)
		}
	}
	if hc.called != 0 {
		t.Errorf("the instance Hardcover wishlist fed a plain user's run (%d calls)", hc.called)
	}
}

// An admin under tenancy, and any user with tenancy off, still get the whole
// instance, the Hardcover wishlist included.
func TestRun_AdminAndTenancyOffUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
		admin   bool
	}{
		{"admin under tenancy", true, true},
		{"user with tenancy off", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			f := newScopeFixture(t)
			uid := f.bob
			if tc.admin {
				uid = f.admin
			}
			hc := &fakeWishlistFetcher{books: []models.RecommendationCandidate{{ForeignID: "hc:wish", Title: "Admin Wishlist Pick", RatingsCount: 500, Rating: 4.5}}}
			got := f.run(t, uid, hc)
			for _, want := range []string{"Alice Secret Title", "Alice Under Shared", "Bob Skipped Title", "Shared Skipped Title", "Admin Wishlist Pick"} {
				if !got[want] {
					t.Errorf("recommendations miss %q: %v", want, got)
				}
			}
			if hc.called != 1 {
				t.Errorf("Hardcover wishlist asked %d times, want 1", hc.called)
			}
		})
	}
}

// Without a user repository the engine cannot tell an admin from a user, so
// a tenancy run is limited to the target's library rather than widened.
func TestRun_TenancyWithoutUserRepoStaysScoped(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newScopeFixture(t)
	ctx := context.Background()
	if err := New(f.books, f.authors, f.series, f.recs, f.settings).Run(ctx, f.bob); err != nil {
		t.Fatal(err)
	}
	got, err := f.recs.List(ctx, f.bob, "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Title == "Alice Secret Title" || r.AuthorName == "Alice Private Author" {
			t.Errorf("unscoped run exposes alice's library: %q by %q", r.Title, r.AuthorName)
		}
	}
}
