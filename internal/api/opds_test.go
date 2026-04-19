package api

import (
	"context"
	"encoding/xml"
	"net"
	"net/http"
	"net/http/httptest"
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

// opdsFixture spins up an in-memory DB, seeds one author + one imported
// book on disk, and returns the wired chi router plus the user repo for
// tests that need to seed credentials.
func opdsFixture(t *testing.T) (*chi.Mux, *db.UserRepo, *db.SettingsRepo, string) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	series := db.NewSeriesRepo(database)
	users := db.NewUserRepo(database)
	settings := db.NewSettingsRepo(database)

	// Build a real file so the FileHandler path works end-to-end.
	tmp := t.TempDir()
	epub := filepath.Join(tmp, "sample.epub")
	if err := os.WriteFile(epub, []byte("fake-epub-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	a := &models.Author{ForeignID: "OL1A", Name: "Ada Palmer", SortName: "Palmer, Ada"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL1W", AuthorID: a.ID, Title: "Too Like the Lightning",
		SortTitle: "too like the lightning",
		Status:    models.BookStatusImported, Language: "eng", Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, b.ID, epub); err != nil {
		t.Fatal(err)
	}

	// Auth bootstrap: seed the three settings so the provider returns
	// sane values (API key, mode, session secret). No users — Basic auth
	// tests add them per-case.
	for _, kv := range [][2]string{
		{SettingAuthAPIKey, "test-api-key"},
		{SettingAuthSessionSecret, "abcdefghijklmnopqrstuvwxyz012345"},
		{SettingAuthMode, string(auth.ModeEnabled)},
	} {
		if err := settings.Set(ctx, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	builder := opds.NewBuilder(opds.Config{PageSize: 50}, books, authors, series)
	fh := NewFileHandler(books)
	h := NewOPDSHandler(builder, books, fh)
	p := &testProvider{settings: settings}

	r := chi.NewRouter()
	r.Route("/opds", func(r chi.Router) {
		r.Use(OPDSAuth(p, users, auth.NewLoginLimiter(5, 15*time.Minute)))
		r.Get("/", h.Root)
		r.Get("/authors", h.Authors)
		r.Get("/authors/{id}", h.Author)
		r.Get("/series", h.Series)
		r.Get("/series/{id}", h.OneSeries)
		r.Get("/recent", h.Recent)
		r.Get("/book/{id}", h.Book)
		r.Get("/book/{id}/file", h.DownloadFile)
	})
	return r, users, settings, "test-api-key"
}

// testProvider mirrors the dbAuthProvider in cmd/bindery — tests can't
// import main.go's struct so we duplicate the minimal interface here.
type testProvider struct {
	settings *db.SettingsRepo
}

func (p *testProvider) Mode() auth.Mode {
	s, _ := p.settings.Get(context.Background(), SettingAuthMode)
	if s == nil {
		return auth.ModeEnabled
	}
	return auth.ParseMode(s.Value)
}
func (p *testProvider) APIKey() string {
	s, _ := p.settings.Get(context.Background(), SettingAuthAPIKey)
	if s == nil {
		return ""
	}
	return s.Value
}
func (p *testProvider) SessionSecret() []byte {
	s, _ := p.settings.Get(context.Background(), SettingAuthSessionSecret)
	if s == nil {
		return nil
	}
	return []byte(s.Value)
}
func (p *testProvider) SetupRequired() bool                        { return false }
func (p *testProvider) ProxyAuthHeader() string                    { return "X-Forwarded-User" }
func (p *testProvider) ProxyAutoProvision() bool                   { return false }
func (p *testProvider) TrustedProxyCIDRs() []*net.IPNet            { return nil }
func (p *testProvider) UserRole(_ context.Context, _ int64) string { return "admin" }
func (p *testProvider) UserProvisioner() auth.UserProvisioner {
	return nil // proxy auth not exercised in these tests
}

// --- tests -------------------------------------------------------------------

func TestOPDS_Unauthenticated_401(t *testing.T) {
	r, _, _, _ := opdsFixture(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/opds/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("missing Basic challenge; got %q", rec.Header().Get("WWW-Authenticate"))
	}
}

func TestOPDS_APIKeyHeaderAllows(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
	req.Header.Set("X-Api-Key", key)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/atom+xml") {
		t.Errorf("content-type = %q", ct)
	}
}

func TestOPDS_APIKeyQueryAllows(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/opds/?apikey="+key, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestOPDS_BasicAuthAllows(t *testing.T) {
	r, users, _, _ := opdsFixture(t)
	hash, err := auth.HashPassword("hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(context.Background(), "admin", hash); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
	req.SetBasicAuth("admin", "hunter2hunter2")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestOPDS_BasicAuthWrongPassword_401(t *testing.T) {
	r, users, _, _ := opdsFixture(t)
	hash, _ := auth.HashPassword("right-password-123")
	_, _ = users.Create(context.Background(), "admin", hash)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
	req.SetBasicAuth("admin", "wrong-password")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestOPDS_Root_Contents(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	body := doOK(t, r, "/opds/", key)
	for _, s := range []string{
		`xmlns="http://www.w3.org/2005/Atom"`,
		"<title>Authors</title>",
		"<title>Series</title>",
		"<title>Recently Added</title>",
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q in root:\n%s", s, body)
		}
	}
}

func TestOPDS_Authors_ListsSeededAuthor(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	body := doOK(t, r, "/opds/authors", key)
	if !strings.Contains(body, "<title>Ada Palmer</title>") {
		t.Errorf("missing author: %s", body)
	}
}

func TestOPDS_Author_HasAcquisitionLink(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	// We know author id=1 (first insert); the body should reference the
	// download path for the single seeded book (id=1).
	body := doOK(t, r, "/opds/authors/1", key)
	if !strings.Contains(body, `/opds/book/1/file`) {
		t.Errorf("missing acquisition link:\n%s", body)
	}
	if !strings.Contains(body, `rel="http://opds-spec.org/acquisition"`) {
		t.Errorf("missing OPDS acquisition rel:\n%s", body)
	}
}

func TestOPDS_Author_NotFound(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opds/authors/9999", nil)
	req.Header.Set("X-Api-Key", key)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestOPDS_Book_DownloadsFile(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opds/book/1/file", nil)
	req.Header.Set("X-Api-Key", key)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "fake-epub-bytes" {
		t.Errorf("body = %q", rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "sample.epub") {
		t.Errorf("content-disposition = %q", cd)
	}
}

func TestOPDS_Recent_ContainsImportedBook(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	body := doOK(t, r, "/opds/recent", key)
	if !strings.Contains(body, "Too Like the Lightning") {
		t.Errorf("recent feed missing book:\n%s", body)
	}
}

func TestOPDS_ResponseIsValidXML(t *testing.T) {
	r, _, _, key := opdsFixture(t)
	body := doOK(t, r, "/opds/authors", key)
	var f opds.Feed
	if err := xml.Unmarshal([]byte(body), &f); err != nil {
		t.Fatalf("invalid xml: %v\n%s", err, body)
	}
	if f.Title == "" {
		t.Error("decoded feed has empty title")
	}
}

func TestOPDS_LocalOnlyMode_BypassesAuth(t *testing.T) {
	r, _, settings, _ := opdsFixture(t)
	_ = settings.Set(context.Background(), SettingAuthMode, string(auth.ModeLocalOnly))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
	req.RemoteAddr = "127.0.0.1:1234" // loopback — local bypass
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d under local-only; want 200", rec.Code)
	}
}

// --- helpers -----------------------------------------------------------------

func doOK(t *testing.T, r http.Handler, path, apiKey string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Api-Key", apiKey)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
