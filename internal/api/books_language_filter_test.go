package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The Books page language filter (#1160): ?language= on the books list and
// GET /book/languages for the options, both scoped like the list itself.

func (f *scopeFixture) setLanguage(t *testing.T, b *models.Book, lang string) {
	t.Helper()
	if err := f.books.SetLanguage(f.ctx, b.ID, lang); err != nil {
		t.Fatal(err)
	}
}

// languageScopeFixture gives alice one Italian book and three with no
// language, and bob one Italian, one English and three with none. Each user
// also has an excluded book with no language, which neither list counts.
func languageScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	f := newScopeFixture(t)
	f.setLanguage(t, f.aliceShared, "ita")
	f.setLanguage(t, f.bobShared, "it-IT")
	f.setLanguage(t, f.bobOwn, "eng")
	return f
}

func TestBookList_LanguageFilter_ScopedToCaller(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := languageScopeFixture(t)

	for _, c := range []struct {
		name  string
		query string
		user  int64
		role  string
		want  []*models.Book
	}{
		{"alice italian", "?language=ita", f.alice, "user", []*models.Book{f.aliceShared}},
		{"alice italian by two letter code", "?language=it", f.alice, "user", []*models.Book{f.aliceShared}},
		{"alice unknown", "?language=unknown", f.alice, "user", []*models.Book{f.aliceDup1, f.aliceDup2, f.aliceOwn}},
		{"alice unknown with search", "?language=unknown&search=emma", f.alice, "user", []*models.Book{f.aliceDup1, f.aliceDup2}},
		{"alice italian and in library", "?language=ita&status=imported", f.alice, "user", nil},
		{"alice italian and wanted ebooks", "?language=ita&status=wanted&mediaType=ebook", f.alice, "user", []*models.Book{f.aliceShared}},
		{"alice english sees none of bob's", "?language=eng", f.alice, "user", nil},
		{"bob italian", "?language=ita", f.bob, "user", []*models.Book{f.bobShared}},
		{"bob unknown", "?language=unknown", f.bob, "user", []*models.Book{f.bobDup1, f.bobPer1, f.bobPer2}},
		{"admin italian", "?language=ita", 99, "admin", []*models.Book{f.aliceShared, f.bobShared}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := f.listBooks(t, c.query+"&limit=500", c.user, c.role)
			if want := scopeIDs(c.want...); !sameScopeIDs(got, want) {
				t.Fatalf("got %v want %v", scopeSorted(got), scopeSorted(want))
			}
		})
	}
}

func (f *scopeFixture) bookLanguages(t *testing.T, user int64, role string) map[string]int {
	t.Helper()
	rec := httptest.NewRecorder()
	f.bookHandlerForScope().Languages(rec, covAuRequest(http.MethodGet, "/api/v1/book/languages", "", nil, user, role))
	if rec.Code != http.StatusOK {
		t.Fatalf("languages: %d %s", rec.Code, rec.Body.String())
	}
	var resp []db.LanguageCount
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("languages body %q: %v", rec.Body.String(), err)
	}
	out := map[string]int{}
	for _, c := range resp {
		out[c.Code] = c.Count
	}
	return out
}

func TestBookLanguages_ScopedToCaller(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := languageScopeFixture(t)

	for _, c := range []struct {
		name string
		user int64
		role string
		want map[string]int
	}{
		{"alice", f.alice, "user", map[string]int{"ita": 1, "": 3}},
		{"bob", f.bob, "user", map[string]int{"ita": 1, "eng": 1, "": 3}},
		{"admin", 99, "admin", map[string]int{"ita": 2, "eng": 1, "": 6}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := f.bookLanguages(t, c.user, c.role)
			if len(got) != len(c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
			for code, n := range c.want {
				if got[code] != n {
					t.Fatalf("got %v want %v", got, c.want)
				}
			}
		})
	}
}
