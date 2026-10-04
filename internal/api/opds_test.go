package api

import (
	"context"
	"encoding/xml"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	// FileHandler fails closed without allowedRoots — seed it with the
	// fixture's tmp dir so the OPDS download path resolves through it.
	fh := NewFileHandler(books, tmp)
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
func (p *testProvider) SessionSecrets() [][]byte {
	secrets := [][]byte{p.SessionSecret()}
	if s, _ := p.settings.Get(context.Background(), SettingAuthSessionSecretPrevious); s != nil && s.Value != "" {
		secrets = append(secrets, []byte(s.Value))
	}
	return secrets
}
func (p *testProvider) SetupRequired() bool                        { return false }
func (p *testProvider) ProxyAuthHeader() string                    { return "X-Forwarded-User" }
func (p *testProvider) ProxyAutoProvision() bool                   { return false }
func (p *testProvider) TrustedProxyCIDRs() []*net.IPNet            { return nil }
func (p *testProvider) UserRole(_ context.Context, _ int64) string { return "admin" }

// OperatorUserID: these OPDS tests assert unauthenticated/api-key behaviour and
// have no users table, so there is no admin to resolve (#1725).
func (p *testProvider) OperatorUserID(context.Context) int64 { return 0 }

// UserSessionEpoch returns 0 here; the OPDS tests in this file mint session
// cookies via SignSession (legacy v2/v1 wrapper) which decode as epoch=0, so
// returning 0 keeps every existing cookie test passing. The dedicated
// password-change/epoch-bump integration tests live elsewhere.
func (p *testProvider) UserSessionEpoch(_ context.Context, _ int64) (int64, error) { return 0, nil }

// SessionRevoked: no logout runs in these tests, so nothing is revoked.
func (p *testProvider) SessionRevoked(_ context.Context, _ string) (bool, error) { return false, nil }
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

// --- #1894: the API-key check must precede the local-only bypass --------------
//
// #1849 was this exact ordering in auth.Middleware: the local-only bypass ran
// ahead of the API-key check, so a request carrying a valid X-Api-Key from a
// trusted LAN address short-circuited before the key was ever verified, never
// got marked AuthedViaAPIKey, and the CSRF guard then rejected its mutations
// with 403. The OPDS subtree is read-only, so here the ordering is inert — and
// that is exactly why it needs pinning: no functional test would notice it
// flipping back, and the first mutating OPDS route would reproduce #1849.

// orderProbeProvider records which provider methods the middleware consults,
// in order. p.APIKey() is reached only from the API-key branch and
// p.TrustedProxyCIDRs() only from the local-only branch, so the recorded
// sequence reads back which of the two ran first.
type orderProbeProvider struct {
	*testProvider
	calls []string
}

func (p *orderProbeProvider) APIKey() string {
	p.calls = append(p.calls, "APIKey")
	return p.testProvider.APIKey()
}

func (p *orderProbeProvider) TrustedProxyCIDRs() []*net.IPNet {
	p.calls = append(p.calls, "TrustedProxyCIDRs")
	return p.testProvider.TrustedProxyCIDRs()
}

// opdsAuthProbe wires OPDSAuth around a trivial handler with an order-probing
// provider in local-only mode. users and limiter are nil: neither the API-key
// nor the local-only branch touches them, and the middleware tolerates nil for
// both, so this stays a test of the precedence chain and nothing else.
func opdsAuthProbe(t *testing.T) (http.Handler, *orderProbeProvider, *bool) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	settings := db.NewSettingsRepo(database)
	for _, kv := range [][2]string{
		{SettingAuthAPIKey, "test-api-key"},
		{SettingAuthMode, string(auth.ModeLocalOnly)},
	} {
		if err := settings.Set(context.Background(), kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	p := &orderProbeProvider{testProvider: &testProvider{settings: settings}}
	served := false
	h := OPDSAuth(p, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))
	return h, p, &served
}

// TestOPDS_LocalOnly_APIKeyCheckedBeforeBypass is the #1894 regression test: a
// valid key from an address local-only already trusts must still be verified
// as a key. The local-only bypass must not get to answer first and hide it.
func TestOPDS_LocalOnly_APIKeyCheckedBeforeBypass(t *testing.T) {
	h, p, served := opdsAuthProbe(t)

	req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
	req.Header.Set("X-Api-Key", "test-api-key")
	req.RemoteAddr = "127.0.0.1:1234" // loopback — local-only would allow this anyway
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !*served {
		t.Fatalf("valid-key request from a trusted-local address must reach the handler; status = %d", rec.Code)
	}
	if len(p.calls) != 1 || p.calls[0] != "APIKey" {
		t.Errorf("provider calls = %v; want [APIKey] — the API-key check must run and answer "+
			"before the local-only bypass, or a verified key goes unnoticed (#1894)", p.calls)
	}
}

// TestOPDS_LocalOnly_BadKeyStillFallsThroughToBypass pins the other half: the
// reordering must not cost a trusted-local caller the bypass it had before. A
// wrong key is not a rejection, it is a miss — the request carries on to the
// local-only branch exactly as it did when that branch ran first.
func TestOPDS_LocalOnly_BadKeyStillFallsThroughToBypass(t *testing.T) {
	h, p, served := opdsAuthProbe(t)

	req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
	req.Header.Set("X-Api-Key", "wrong-key")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !*served {
		t.Fatalf("wrong key must fall through to the local-only bypass, not reject; status = %d", rec.Code)
	}
	if len(p.calls) != 2 || p.calls[0] != "APIKey" || p.calls[1] != "TrustedProxyCIDRs" {
		t.Errorf("provider calls = %v; want [APIKey TrustedProxyCIDRs] (#1894)", p.calls)
	}
}

// TestOPDS_LocalOnly_KeyWorksFromUntrustedAddress guards the case the bypass
// never covered: off-LAN callers depend on the key branch alone, so it must
// keep answering for them after the move.
func TestOPDS_LocalOnly_KeyWorksFromUntrustedAddress(t *testing.T) {
	h, _, served := opdsAuthProbe(t)

	req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
	req.Header.Set("X-Api-Key", "test-api-key")
	req.RemoteAddr = "203.0.113.7:5555" // public address — no local-only bypass
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !*served {
		t.Fatalf("valid key from an untrusted address must still be allowed; status = %d", rec.Code)
	}
}

// --- D3 per-user scoping -----------------------------------------------------

// TestOPDS_FeedFiltersToCallerLibraryWhenGateOn verifies the scoped OPDS
// feed only shows the basic-auth caller's books. Without this, every user
// with OPDS access could enumerate every other user's library.
func TestOPDS_FeedFiltersToCallerLibraryWhenGateOn(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	series := db.NewSeriesRepo(database)
	users := db.NewUserRepo(database)
	settings := db.NewSettingsRepo(database)

	tmp := t.TempDir()
	aliceEpub := filepath.Join(tmp, "alice.epub")
	bobEpub := filepath.Join(tmp, "bob.epub")
	if err := os.WriteFile(aliceEpub, []byte("alice"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bobEpub, []byte("bob"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Two users, two libraries.
	hashA, _ := auth.HashPassword("alicepassword12")
	uA, err := users.Create(ctx, "alice", hashA)
	if err != nil {
		t.Fatal(err)
	}
	hashB, _ := auth.HashPassword("bobpassword12345")
	uB, err := users.Create(ctx, "bob", hashB)
	if err != nil {
		t.Fatal(err)
	}

	aA := &models.Author{ForeignID: "OL-A", Name: "Author Alice", SortName: "Alice, Author"}
	if err := authors.CreateForUser(ctx, aA, uA.ID); err != nil {
		t.Fatal(err)
	}
	aB := &models.Author{ForeignID: "OL-B", Name: "Author Bob", SortName: "Bob, Author"}
	if err := authors.CreateForUser(ctx, aB, uB.ID); err != nil {
		t.Fatal(err)
	}
	bA := &models.Book{ForeignID: "W-A", AuthorID: aA.ID, Title: "Alice Only Book", SortTitle: "alice only book", Status: models.BookStatusImported, Monitored: true}
	if err := books.Create(ctx, bA); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", uA.ID, bA.ID); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, bA.ID, aliceEpub); err != nil {
		t.Fatal(err)
	}
	bB := &models.Book{ForeignID: "W-B", AuthorID: aB.ID, Title: "Bob Only Book", SortTitle: "bob only book", Status: models.BookStatusImported, Monitored: true}
	if err := books.Create(ctx, bB); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", uB.ID, bB.ID); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, bB.ID, bobEpub); err != nil {
		t.Fatal(err)
	}

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
	fh := NewFileHandler(books, tmp)
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

	doAs := func(path, user, pass string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth(user, pass)
		r.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// Bob hits /opds/authors — must only see his own author, not alice's.
	code, body := doAs("/opds/authors", "bob", "bobpassword12345")
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if strings.Contains(body, "Author Alice") {
		t.Errorf("bob's authors feed leaked alice's author:\n%s", body)
	}
	if !strings.Contains(body, "Author Bob") {
		t.Errorf("bob's authors feed missing his own author:\n%s", body)
	}

	// Bob hits /opds/recent — only bob's book.
	code, body = doAs("/opds/recent", "bob", "bobpassword12345")
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if strings.Contains(body, "Alice Only Book") {
		t.Errorf("bob's recent feed leaked alice's book:\n%s", body)
	}
	if !strings.Contains(body, "Bob Only Book") {
		t.Errorf("bob's recent feed missing his own book:\n%s", body)
	}

	// Bob requests alice's book by id — must 404 even though the row exists.
	code, body = doAs("/opds/book/"+strconv.FormatInt(bA.ID, 10), "bob", "bobpassword12345")
	if code != http.StatusNotFound {
		t.Errorf("bob fetching alice's book detail: want 404, got %d body=%s", code, body)
	}

	// Bob downloads alice's book file — must 404, not stream the bytes.
	code, body = doAs("/opds/book/"+strconv.FormatInt(bA.ID, 10)+"/file", "bob", "bobpassword12345")
	if code != http.StatusNotFound {
		t.Errorf("bob downloading alice's book file: want 404, got %d body=%s", code, body)
	}
	_ = uB
}

// TestOPDS_GateOffPreservesLegacyBehavior — the env-gate canary for OPDS:
// with the gate off, the existing "everyone sees everything" behaviour
// (pre-D3) still applies. This is intentional for single-user installs that
// haven't opted in.
func TestOPDS_GateOffPreservesLegacyBehavior(t *testing.T) {
	// Default: gate off.
	r, _, _, key := opdsFixture(t)
	body := doOK(t, r, "/opds/recent", key)
	if !strings.Contains(body, "Too Like the Lightning") {
		t.Errorf("legacy: recent feed should include the seeded book; got:\n%s", body)
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
