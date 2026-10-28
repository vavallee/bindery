package api

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/opds"
)

func TestOPDSSearch_RootAdvertisesSearch(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	body := doOK(t, r, "/opds/", key)
	if !strings.Contains(body, `rel="search"`) ||
		!strings.Contains(body, `type="application/opensearchdescription+xml"`) ||
		!strings.Contains(body, `/opds/opensearch.xml`) ||
		!strings.Contains(body, `/opds/search?q={searchTerms}`) {
		t.Errorf("root feed lacks search links:\n%s", body)
	}
}

func TestOPDSSearch_OpenSearchDocument(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opds/opensearch.xml", nil)
	req.Header.Set("X-Api-Key", key)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/opensearchdescription+xml") {
		t.Errorf("content-type = %q", ct)
	}
	var d opds.OpenSearchDescription
	if err := xml.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("invalid xml: %v", err)
	}
	if len(d.URLs) != 1 || !strings.Contains(d.URLs[0].Template, "/opds/search?q={searchTerms}") {
		t.Errorf("urls = %+v", d.URLs)
	}
}

func TestOPDSSearch_FindsBook(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	body := doOK(t, r, "/opds/search?q="+url.QueryEscape("lightning palmer"), key)
	if !strings.Contains(body, "Too Like the Lightning") || !strings.Contains(body, "Ada Palmer") {
		t.Errorf("match missing:\n%s", body)
	}
	if !strings.Contains(body, "/opds/book/1/file") {
		t.Errorf("no acquisition link:\n%s", body)
	}
	if strings.Contains(doOK(t, r, "/opds/search?q=zzzznomatch", key), "<entry>") {
		t.Error("non matching query returned entries")
	}
}

func TestOPDSSearch_EmptyAndPunctuationQueriesReturnNothing(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	for _, q := range []string{"", "%20%20", url.QueryEscape("???")} {
		body := doOK(t, r, "/opds/search?q="+q, key)
		if strings.Contains(body, "<entry>") {
			t.Errorf("q=%q returned the library:\n%s", q, body)
		}
	}
}

func TestOPDSSearch_UnicodeAccentFolded(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	// Seeded after the fixture, through the same router's DB is not reachable,
	// so query the fixture title with accents added to the query side.
	body := doOK(t, r, "/opds/search?q="+url.QueryEscape("Tóo Líke the Lïghtning"), key)
	if !strings.Contains(body, "Too Like the Lightning") {
		t.Errorf("accent folded query found nothing:\n%s", body)
	}
	if _, err := xmlWellFormed(doOK(t, r, "/opds/search?q="+url.QueryEscape("日本語 😀 <&>"), key)); err != nil {
		t.Errorf("unicode query gave bad xml: %v", err)
	}
}

func xmlWellFormed(s string) (struct{}, error) {
	return struct{}{}, xml.Unmarshal([]byte(s), new(opds.Feed))
}

func TestOPDSSearch_QueryTooLongIs400(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opds/search?q="+strings.Repeat("a", opds.MaxSearchQueryLen+1), nil)
	req.Header.Set("X-Api-Key", key)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
}

func TestOPDSSearch_RequiresAuth(t *testing.T) {
	r, _, _, _ := opdsFixture(t)
	for _, p := range []string{"/opds/search?q=lightning", "/opds/opensearch.xml"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated = %d, want 401", p, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no Basic challenge", p)
		}
	}
}

// Two users with the same word in their titles: with tenancy on, each sees
// only their own book, and a query naming the other's author finds nothing.
func TestOPDSSearch_ScopedToCaller(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	authors, books, series := db.NewAuthorRepo(database), db.NewBookRepo(database), db.NewSeriesRepo(database)
	users, settings := db.NewUserRepo(database), db.NewSettingsRepo(database)
	tmp := t.TempDir()

	seed := func(name, pass, title, author string) {
		hash, _ := auth.HashPassword(pass)
		u, err := users.Create(ctx, name, hash)
		if err != nil {
			t.Fatal(err)
		}
		a := &models.Author{ForeignID: "A-" + name, Name: author, SortName: author}
		if err := authors.CreateForUser(ctx, a, u.ID); err != nil {
			t.Fatal(err)
		}
		bk := &models.Book{ForeignID: "W-" + name, AuthorID: a.ID, Title: title, SortTitle: title, Status: models.BookStatusImported, Monitored: true}
		if err := books.Create(ctx, bk); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", u.ID, bk.ID); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(tmp, name+".epub")
		if err := os.WriteFile(f, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := books.SetFilePath(ctx, bk.ID, f); err != nil {
			t.Fatal(err)
		}
	}
	seed("alice", "alicepassword12", "Dragon Atlas of Alice", "Zelda Quill")
	seed("bob", "bobpassword12345", "Dragon Atlas of Bob", "Yusuf Reed")
	for _, kv := range [][2]string{
		{SettingAuthAPIKey, "test-api-key"},
		{SettingAuthSessionSecret, "abcdefghijklmnopqrstuvwxyz012345"},
		{SettingAuthMode, string(auth.ModeEnabled)},
	} {
		if err := settings.Set(ctx, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	h := NewOPDSHandler(opds.NewBuilder(opds.Config{PageSize: 50}, books, authors, series), books, NewFileHandler(books, tmp))
	r := chi.NewRouter()
	r.Route("/opds", func(r chi.Router) {
		r.Use(OPDSAuth(&testProvider{settings: settings}, users, auth.NewLoginLimiter(5, 15*time.Minute)))
		r.Get("/search", h.Search)
	})
	as := func(user, pass, q string) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/opds/search?q="+url.QueryEscape(q), nil)
		req.SetBasicAuth(user, pass)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := as("bob", "bobpassword12345", "dragon atlas")
	if !strings.Contains(body, "Dragon Atlas of Bob") || strings.Contains(body, "of Alice") {
		t.Errorf("bob's search not scoped:\n%s", body)
	}
	body = as("alice", "alicepassword12", "dragon atlas")
	if !strings.Contains(body, "Dragon Atlas of Alice") || strings.Contains(body, "of Bob") {
		t.Errorf("alice's search not scoped:\n%s", body)
	}
	// Searching by the other user's author name must not reveal that book.
	if strings.Contains(as("bob", "bobpassword12345", "Zelda Quill"), "<entry>") {
		t.Error("bob found alice's book by author name")
	}
}
