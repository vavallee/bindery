package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// librarySearchFixture seeds two users with disjoint libraries so every test
// can assert both the match and the scope in one call:
//
//	alice: author "Ursula K. Le Guin" (alias "U. K. Le Guin"), books "A Wizard
//	       of Earthsea" and "The Left Hand of Darkness", series "Earthsea Cycle"
//	bob:   author "Brandon Sanderson", book "The Way of Kings", series
//	       "The Stormlight Archive"
//
// Tenancy is forced on so ListScopeUserID scopes non-admin callers.
type librarySearchFixture struct {
	handler *LibrarySearchHandler
	alice   *db.User
	bob     *db.User
	authors *db.AuthorRepo
	books   *db.BookRepo
	series  *db.SeriesRepo
	ctx     context.Context
}

func newLibrarySearchFixture(t *testing.T) *librarySearchFixture {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, true)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	f := &librarySearchFixture{
		alice:   alice,
		bob:     bob,
		authors: db.NewAuthorRepo(database),
		books:   db.NewBookRepo(database),
		series:  db.NewSeriesRepo(database),
		ctx:     ctx,
	}
	f.handler = NewLibrarySearchHandler(f.authors, f.books, f.series)
	aliases := db.NewAuthorAliasRepo(database)

	leGuin := f.seedAuthor(t, "OL-leguin", "Ursula K. Le Guin", alice.ID)
	if err := aliases.Create(ctx, &models.AuthorAlias{AuthorID: leGuin.ID, Name: "U. K. Le Guin"}); err != nil {
		t.Fatalf("create alias: %v", err)
	}
	wizard := f.seedBook(t, "OL-wizard", leGuin.ID, "A Wizard of Earthsea", alice.ID)
	f.seedBook(t, "OL-lefthand", leGuin.ID, "The Left Hand of Darkness", alice.ID)
	f.seedSeries(t, "hc:earthsea", "Earthsea Cycle", wizard.ID)

	sanderson := f.seedAuthor(t, "OL-sanderson", "Brandon Sanderson", bob.ID)
	kings := f.seedBook(t, "OL-kings", sanderson.ID, "The Way of Kings", bob.ID)
	f.seedSeries(t, "hc:stormlight", "The Stormlight Archive", kings.ID)
	return f
}

func (f *librarySearchFixture) seedAuthor(t *testing.T, foreignID, name string, owner int64) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: foreignID, Name: name, SortName: name, MetadataProvider: "openlibrary", Monitored: true}
	if err := f.authors.CreateForUser(f.ctx, a, owner); err != nil {
		t.Fatalf("seed author %q: %v", name, err)
	}
	return a
}

func (f *librarySearchFixture) seedBook(t *testing.T, foreignID string, authorID int64, title string, owner int64) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: foreignID, AuthorID: authorID, Title: title, SortTitle: title, Status: "wanted",
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: owner,
	}
	if err := f.books.Create(f.ctx, b); err != nil {
		t.Fatalf("seed book %q: %v", title, err)
	}
	return b
}

func (f *librarySearchFixture) seedSeries(t *testing.T, foreignID, title string, bookID int64) *models.Series {
	t.Helper()
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := f.series.Create(f.ctx, s); err != nil {
		t.Fatalf("seed series %q: %v", title, err)
	}
	if err := f.series.LinkBook(f.ctx, s.ID, bookID, "1", true); err != nil {
		t.Fatalf("link series %q: %v", title, err)
	}
	return s
}

// search runs the handler as userID, a non-admin.
func (f *librarySearchFixture) search(t *testing.T, userID int64, query string) (int, librarySearchResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search/library?"+query, nil)
	req = req.WithContext(auth.WithUserID(req.Context(), userID))
	rec := httptest.NewRecorder()
	f.handler.Search(rec, req)
	var body librarySearchResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestLibrarySearch_MatchesBookTitle(t *testing.T) {
	f := newLibrarySearchFixture(t)
	code, got := f.search(t, f.alice.ID, "q=wizard")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if len(got.Books) != 1 || got.Books[0].Title != "A Wizard of Earthsea" {
		t.Fatalf("expected the Earthsea book, got %+v", got.Books)
	}
	if got.Books[0].AuthorName != "Ursula K. Le Guin" || got.Books[0].AuthorID == 0 {
		t.Errorf("book row should carry its author, got %+v", got.Books[0])
	}
	if len(got.Authors) != 0 {
		t.Errorf("no author is called wizard, got %+v", got.Authors)
	}
}

func TestLibrarySearch_MatchesAuthorNameAndTheirBooks(t *testing.T) {
	f := newLibrarySearchFixture(t)
	_, got := f.search(t, f.alice.ID, "q=le+guin")
	if len(got.Authors) != 1 || got.Authors[0].Name != "Ursula K. Le Guin" {
		t.Fatalf("expected Le Guin in authors, got %+v", got.Authors)
	}
	// Books match on the author's name too, as on the Books page.
	if len(got.Books) != 2 {
		t.Errorf("expected both Le Guin books via the author name, got %+v", got.Books)
	}
}

func TestLibrarySearch_MatchesAuthorAlias(t *testing.T) {
	f := newLibrarySearchFixture(t)
	// "u k" is only contiguous in the alias "U. K. Le Guin" once folded; the
	// canonical name has "ursula k".
	_, got := f.search(t, f.alice.ID, "q=u+k+le")
	if len(got.Authors) != 1 || got.Authors[0].Name != "Ursula K. Le Guin" {
		t.Fatalf("expected the alias to surface Le Guin, got %+v", got.Authors)
	}
}

func TestLibrarySearch_MatchesSeriesTitle(t *testing.T) {
	f := newLibrarySearchFixture(t)
	_, got := f.search(t, f.alice.ID, "q=earthsea")
	if len(got.Series) != 1 || got.Series[0].Title != "Earthsea Cycle" {
		t.Fatalf("expected the Earthsea series, got %+v", got.Series)
	}
	if len(got.Books) != 1 {
		t.Errorf("the book title also contains earthsea, got %+v", got.Books)
	}
}

func TestLibrarySearch_SeriesFoldsDiacritics(t *testing.T) {
	f := newLibrarySearchFixture(t)
	a := f.seedAuthor(t, "OL-ost-author", "Standalone Author", f.alice.ID)
	s := f.seedSeries(t, "hc:ostergaard", "Østergaard Chronicles", f.seedBook(t, "OL-ost", a.ID, "Standalone", f.alice.ID).ID)
	_, got := f.search(t, f.alice.ID, "q=ostergaard")
	if len(got.Series) != 1 || got.Series[0].ID != s.ID {
		t.Fatalf("expected the folded series match, got %+v", got.Series)
	}
}

// TestLibrarySearch_OwnerScoped is the multi-user guarantee: a user's search
// never reveals another user's authors, books or series, even on an exact
// query for them.
func TestLibrarySearch_OwnerScoped(t *testing.T) {
	f := newLibrarySearchFixture(t)
	for _, q := range []string{"q=sanderson", "q=way+of+kings", "q=stormlight"} {
		_, got := f.search(t, f.alice.ID, q)
		if len(got.Authors)+len(got.Books)+len(got.Series) != 0 {
			t.Errorf("%s: alice can see bob's rows: %+v", q, got)
		}
	}
	// Bob finds them.
	_, got := f.search(t, f.bob.ID, "q=stormlight")
	if len(got.Series) != 1 {
		t.Errorf("bob should see his own series, got %+v", got.Series)
	}
	// An admin is unscoped and sees the shared library.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search/library?q=stormlight", nil)
	req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), f.alice.ID), "admin"))
	rec := httptest.NewRecorder()
	f.handler.Search(rec, req)
	var admin librarySearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &admin); err != nil {
		t.Fatal(err)
	}
	if len(admin.Series) != 1 {
		t.Errorf("admin should be unscoped, got %+v", admin.Series)
	}
}

func TestLibrarySearch_EmptyQueryIs400(t *testing.T) {
	f := newLibrarySearchFixture(t)
	for _, q := range []string{"", "q=", "q=%20%20"} {
		code, _ := f.search(t, f.alice.ID, q)
		if code != http.StatusBadRequest {
			t.Errorf("%q: expected 400, got %d", q, code)
		}
	}
}

// TestLibrarySearch_QueryTooLongIs400 pins the byte cap on q. Every token
// becomes a LIKE pair and a single long token becomes a long LIKE pattern, so
// without the cap a 100 KB query reached SQLite's limits and came back 500.
func TestLibrarySearch_QueryTooLongIs400(t *testing.T) {
	f := newLibrarySearchFixture(t)
	oneToken := "q=" + strings.Repeat("a", 100*1024)
	manyTokens := "q=" + strings.TrimSuffix(strings.Repeat("ab+", 40*1024), "+")
	atCap := "q=" + strings.Repeat("a", librarySearchMaxQueryBytes)
	overCap := atCap + "a"
	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"single 100 KB token", oneToken, http.StatusBadRequest},
		{"100 KB of short tokens", manyTokens, http.StatusBadRequest},
		{"one byte over the cap", overCap, http.StatusBadRequest},
		{"exactly at the cap", atCap, http.StatusOK},
	} {
		code, _ := f.search(t, f.alice.ID, tc.query)
		if code != tc.want {
			t.Errorf("%s: expected %d, got %d", tc.name, tc.want, code)
		}
	}
}

// TestLibrarySearch_PunctuationOnlyIsEmpty covers a query that folds to
// nothing. The list repositories treat an empty fold as "no filter" and
// return the top of the whole library, which for a typeahead would mean "?"
// lists five of everything. The handler answers with empty groups instead,
// as a 200, so the client shows only the add row.
func TestLibrarySearch_PunctuationOnlyIsEmpty(t *testing.T) {
	f := newLibrarySearchFixture(t)
	for _, q := range []string{"q=%3F", "q=...", "q=%25", "q=_", "q=%3F%3F%3F"} {
		code, got := f.search(t, f.alice.ID, q)
		if code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", q, code)
			continue
		}
		if len(got.Authors)+len(got.Books)+len(got.Series) != 0 {
			t.Errorf("%s: a query with no search terms must match nothing, got %+v", q, got)
		}
	}
}

func TestLibrarySearch_NoMatchesReturnsEmptyArrays(t *testing.T) {
	f := newLibrarySearchFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search/library?q=zzzz", nil)
	req = req.WithContext(auth.WithUserID(req.Context(), f.alice.ID))
	rec := httptest.NewRecorder()
	f.handler.Search(rec, req)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"authors", "books", "series"} {
		if string(raw[k]) != "[]" {
			t.Errorf("%s should be [] not %s so the client never null-checks", k, raw[k])
		}
	}
}

func TestLibrarySearch_LimitDefaultsAndClamps(t *testing.T) {
	f := newLibrarySearchFixture(t)
	author := f.seedAuthor(t, "OL-prolific", "Prolific Writer", f.alice.ID)
	for i := 0; i < 12; i++ {
		f.seedBook(t, fmt.Sprintf("OL-vol-%d", i), author.ID, fmt.Sprintf("Volume %d", i), f.alice.ID)
	}
	cases := map[string]int{
		"q=volume":          librarySearchDefaultLimit,
		"q=volume&limit=3":  3,
		"q=volume&limit=50": librarySearchMaxLimit,
		"q=volume&limit=0":  librarySearchDefaultLimit,
		"q=volume&limit=x":  librarySearchDefaultLimit,
	}
	for q, want := range cases {
		_, got := f.search(t, f.alice.ID, q)
		if len(got.Books) != want {
			t.Errorf("%s: expected %d books, got %d", q, want, len(got.Books))
		}
	}
}

// TestLibrarySearch_RanksExactSeriesFirst pins the ranking so a whole-title
// hit is not buried under a longer title that merely contains the word.
//
// "Dunes" is the case that proves the tiers are doing the work: it is the
// shortest title in the set, so a length-only order would put it first, but
// it only matches "dune" as the start of a longer word (tier 3), below every
// title that has "dune" as a whole word.
func TestLibrarySearch_RanksExactSeriesFirst(t *testing.T) {
	f := newLibrarySearchFixture(t)
	a := f.seedAuthor(t, "OL-herbert", "Frank Herbert", f.alice.ID)
	b := f.seedBook(t, "OL-dune", a.ID, "Dune", f.alice.ID)
	f.seedSeries(t, "hc:dunes", "Dunes", b.ID)
	f.seedSeries(t, "hc:dune-prequels", "Legends of Dune", b.ID)
	f.seedSeries(t, "hc:dune", "Dune", b.ID)
	f.seedSeries(t, "hc:dune-chronicles", "Dune Chronicles", b.ID)
	_, got := f.search(t, f.alice.ID, "q=dune")
	if len(got.Series) != 4 {
		t.Fatalf("expected four series, got %+v", got.Series)
	}
	want := []string{"Dune", "Dune Chronicles", "Legends of Dune", "Dunes"}
	for i, w := range want {
		if got.Series[i].Title != w {
			t.Errorf("rank %d: want %q, got %q", i, w, got.Series[i].Title)
		}
	}
}
