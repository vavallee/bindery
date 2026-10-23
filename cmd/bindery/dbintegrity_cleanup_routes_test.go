package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// cleanupStack is the real auth middleware, the real CSRF guards, the real
// route registration, a real database file and the real backup handler. A
// stub handler behind a stub router missed a real bug in part 1.
type cleanupStack struct {
	t        *testing.T
	router   http.Handler
	handler  *api.DBIntegrityHandler
	settings *db.SettingsRepo
	secret   string
	admin    *http.Cookie
	user     *http.Cookie
	dataDir  string
}

func newCleanupStack(t *testing.T, orphans int) *cleanupStack {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bindery.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx := context.Background()
	settings := db.NewSettingsRepo(conn)
	users := db.NewUserRepo(conn)
	secret := strings.Repeat("s", 32)
	if err := settings.Set(ctx, api.SettingAuthSessionSecret, secret); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, api.SettingAuthMode, string(auth.ModeEnabled)); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(ctx, "admin", "x"); err != nil {
		t.Fatal(err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(ctx, "bob", "x"); err != nil {
		t.Fatal(err)
	}

	if _, err := conn.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < orphans; i++ {
		if _, err := conn.Exec(fmt.Sprintf(`INSERT INTO book_files (book_id, format, path) VALUES (%d, 'ebook', '/l/%d.epub')`, 5000+i, i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	h := api.NewDBIntegrityHandler(conn, path, api.NewBackupHandler(conn, path, dir))
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		useAPIAuth(r, &dbAuthProvider{settings: settings, users: users})
		registerDBIntegrityRoutes(r, h)
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})

	cookieFor := func(uid int64) *http.Cookie {
		v, err := auth.SignSessionWithEpoch([]byte(secret), uid, 1, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: auth.SessionCookieName, Value: v}
	}
	return &cleanupStack{
		t: t, router: router, handler: h, settings: settings, secret: secret,
		admin: cookieFor(1), user: cookieFor(2), dataDir: dir,
	}
}

type reqOpt struct {
	cookie *http.Cookie
	noXRW  bool
	noCSRF bool
}

func (s *cleanupStack) do(method, target, body string, o reqOpt) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "203.0.113.9:4000"
	req.Host = "bindery:8787"
	if o.cookie != nil {
		req.AddCookie(o.cookie)
		if method != http.MethodGet {
			if !o.noXRW {
				req.Header.Set("X-Requested-With", "bindery-ui")
			}
			if !o.noCSRF {
				req.Header.Set("X-CSRF-Token", auth.MakeCSRFToken([]byte(s.secret), o.cookie.Value))
			}
		}
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

const (
	pCheck   = "/api/v1/system/db-integrity"
	pPreview = "/api/v1/system/db-integrity/cleanup/preview"
	pCleanup = "/api/v1/system/db-integrity/cleanup"
)

func (s *cleanupStack) previewToken() string {
	s.t.Helper()
	rec := s.do(http.MethodPost, pPreview, "", reqOpt{cookie: s.admin})
	if rec.Code != http.StatusOK {
		s.t.Fatalf("preview = %d %s", rec.Code, rec.Body.String())
	}
	var p struct {
		PreviewToken string `json:"previewToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.PreviewToken == "" {
		s.t.Fatalf("no token in %s (%v)", rec.Body.String(), err)
	}
	return p.PreviewToken
}

func confirm(token string) string {
	b, _ := json.Marshal(map[string]any{"confirm": true, "previewToken": token})
	return string(b)
}

// Every cleanup route is admin only and behind the CSRF guards, through the
// real stack: no session is 401, a plain user is 403, a missing header or
// token is refused even for the admin, and none of those get as far as the
// handler (nothing was repaired).
func TestDBCleanupRoutesThroughAuthStack(t *testing.T) {
	s := newCleanupStack(t, 3)
	token := s.previewToken()
	body := confirm(token)

	for _, path := range []string{pPreview, pCleanup} {
		if rec := s.do(http.MethodPost, path, body, reqOpt{}); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated = %d, want 401", path, rec.Code)
		}
		rec := s.do(http.MethodPost, path, body, reqOpt{cookie: s.user})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as a plain user = %d, want 403", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "backup") || strings.Contains(rec.Body.String(), s.dataDir) {
			t.Errorf("%s leaked to a non admin: %s", path, rec.Body.String())
		}
		if rec := s.do(http.MethodPost, path, body, reqOpt{cookie: s.admin, noCSRF: true}); rec.Code != http.StatusForbidden {
			t.Errorf("%s without the CSRF token = %d, want 403", path, rec.Code)
		}
		if rec := s.do(http.MethodPost, path, body, reqOpt{cookie: s.admin, noXRW: true}); rec.Code != http.StatusForbidden {
			t.Errorf("%s without X-Requested-With = %d, want 403", path, rec.Code)
		}
		if rec := s.do(http.MethodGet, path, "", reqOpt{cookie: s.admin}); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 405 or 404", path, rec.Code)
		}
	}
	// A stray admin POST without confirmation does nothing.
	if rec := s.do(http.MethodPost, pCleanup, "", reqOpt{cookie: s.admin}); rec.Code != http.StatusBadRequest {
		t.Errorf("stray POST = %d, want 400", rec.Code)
	}
	if rec := s.do(http.MethodPost, pCleanup, `{"confirm":true}`, reqOpt{cookie: s.admin}); rec.Code != http.StatusBadRequest {
		t.Errorf("confirm without token = %d, want 400", rec.Code)
	}
	matches, _ := filepath.Glob(filepath.Join(s.dataDir, "backups", "*"))
	if len(matches) != 0 {
		t.Errorf("a refused request still wrote a backup: %v", matches)
	}
	// And the admin with a proper confirmation succeeds.
	rec := s.do(http.MethodPost, pCleanup, body, reqOpt{cookie: s.admin})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin cleanup = %d %s", rec.Code, rec.Body.String())
	}
}

// While a cleanup is in flight, a second cleanup, a preview and a check each
// get 409 straight away, and the app keeps answering: the cleanup does not hold
// the shared connection between its batches. All through the real stack.
func TestDBCleanupSingleFlightAndAppStaysUpThroughAuthStack(t *testing.T) {
	// More than one batch (500 rows each), so the hook fires mid repair.
	s := newCleanupStack(t, 1300)
	token := s.previewToken()

	inRepair := make(chan struct{})
	release := make(chan struct{})
	first := true
	s.handler.SetRepairHook(func() {
		if first {
			first = false
			close(inRepair)
			<-release
		}
	})

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- s.do(http.MethodPost, pCleanup, confirm(token), reqOpt{cookie: s.admin}) }()
	select {
	case <-inRepair:
	case <-time.After(60 * time.Second):
		t.Fatal("repair never reached its first batch boundary")
	}
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer releaseOnce()

	within := func(name string, f func()) {
		t.Helper()
		fin := make(chan struct{})
		go func() { f(); close(fin) }()
		select {
		case <-fin:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not complete within 3s while a cleanup was running", name)
		}
	}
	within("a settings read on the shared pool", func() {
		if _, err := s.settings.Get(context.Background(), api.SettingAuthMode); err != nil {
			t.Error(err)
		}
	})
	within("an authenticated GET /health", func() {
		if rec := s.do(http.MethodGet, "/api/v1/health", "", reqOpt{cookie: s.admin}); rec.Code != http.StatusNoContent {
			t.Errorf("health = %d", rec.Code)
		}
	})
	within("a second cleanup", func() {
		if rec := s.do(http.MethodPost, pCleanup, confirm(token), reqOpt{cookie: s.admin}); rec.Code != http.StatusConflict {
			t.Errorf("second cleanup = %d %s, want 409", rec.Code, rec.Body.String())
		}
	})
	within("a preview", func() {
		if rec := s.do(http.MethodPost, pPreview, "", reqOpt{cookie: s.admin}); rec.Code != http.StatusConflict {
			t.Errorf("preview = %d, want 409", rec.Code)
		}
	})
	within("a check", func() {
		if rec := s.do(http.MethodPost, pCheck, "", reqOpt{cookie: s.admin}); rec.Code != http.StatusConflict {
			t.Errorf("check = %d, want 409", rec.Code)
		}
	})

	releaseOnce()
	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("first cleanup = %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Deleted        int  `json:"deleted"`
		RemainingTotal int  `json:"remainingTotal"`
		Verified       bool `json:"verified"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Deleted != 1300 || resp.RemainingTotal != 0 || !resp.Verified {
		t.Errorf("result = %+v, want 1300 deleted and a clean verify", resp)
	}
	// The lock is free again.
	if rec := s.do(http.MethodPost, pCheck, "", reqOpt{cookie: s.admin}); rec.Code != http.StatusOK {
		t.Errorf("check after the cleanup = %d, want 200", rec.Code)
	}
}
