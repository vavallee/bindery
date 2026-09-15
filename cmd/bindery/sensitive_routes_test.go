package main

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubSensitiveHandler stands in for the indexer, prowlarr, and download
// client handlers in route-auth tests. Every method records its name and
// writes 204; if RequireAdmin rejects the request the handler never runs,
// so an empty `called` slice with status 403 is the regression-free path.
type stubSensitiveHandler struct {
	called []string
}

func (h *stubSensitiveHandler) record(name string, w http.ResponseWriter) {
	h.called = append(h.called, name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubSensitiveHandler) List(w http.ResponseWriter, _ *http.Request) {
	h.record("list", w)
}

func (h *stubSensitiveHandler) Get(w http.ResponseWriter, _ *http.Request) {
	h.record("get", w)
}

func (h *stubSensitiveHandler) Create(w http.ResponseWriter, _ *http.Request) {
	h.record("create", w)
}

func (h *stubSensitiveHandler) Update(w http.ResponseWriter, _ *http.Request) {
	h.record("update", w)
}

func (h *stubSensitiveHandler) Delete(w http.ResponseWriter, _ *http.Request) {
	h.record("delete", w)
}

func (h *stubSensitiveHandler) Test(w http.ResponseWriter, _ *http.Request) {
	h.record("test", w)
}

func (h *stubSensitiveHandler) TestConfig(w http.ResponseWriter, _ *http.Request) {
	h.record("test-config", w)
}

func (h *stubSensitiveHandler) Sync(w http.ResponseWriter, _ *http.Request) {
	h.record("sync", w)
}

func (h *stubSensitiveHandler) SearchQuery(w http.ResponseWriter, _ *http.Request) {
	h.record("search-query", w)
}

func (h *stubSensitiveHandler) LastSearchDebug(w http.ResponseWriter, _ *http.Request) {
	h.record("last-search-debug", w)
}

func (h *stubSensitiveHandler) ImportCSV(w http.ResponseWriter, _ *http.Request) {
	h.record("import-csv", w)
}

func (h *stubSensitiveHandler) ImportReadarr(w http.ResponseWriter, _ *http.Request) {
	h.record("import-readarr", w)
}

func (h *stubSensitiveHandler) ImportReadarrStatus(w http.ResponseWriter, _ *http.Request) {
	h.record("import-readarr-status", w)
}

func (h *stubSensitiveHandler) ImportGoodreadsPreview(w http.ResponseWriter, _ *http.Request) {
	h.record("import-goodreads-preview", w)
}

func (h *stubSensitiveHandler) ImportGoodreadsCommit(w http.ResponseWriter, _ *http.Request) {
	h.record("import-goodreads-commit", w)
}

func (h *stubSensitiveHandler) Export(w http.ResponseWriter, _ *http.Request) {
	h.record("export-logs", w)
}

func (h *stubSensitiveHandler) TestDiscovery(w http.ResponseWriter, _ *http.Request) {
	h.record("oidc-test-discovery", w)
}

func (h *stubSensitiveHandler) GetLevel(w http.ResponseWriter, _ *http.Request) {
	h.record("get-level", w)
}

func (h *stubSensitiveHandler) SetLevel(w http.ResponseWriter, _ *http.Request) {
	h.record("set-level", w)
}

// TestSystemLogRoutesRequireAdmin locks in the sweep finding that
// /system/logs and /system/loglevel were mounted outside any admin Group, so
// role=user could read the app-wide log stream (other users' titles, OIDC
// usernames) and flip the global log level. A non-admin must be rejected
// before the handler runs.
func TestSystemLogRoutesRequireAdmin(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "read logs", method: http.MethodGet, path: "/system/logs"},
		// The export is the same data as a downloadable file (#1903), so it
		// must sit behind the same gate — a non-admin walking out with a text
		// dump of the app-wide log is the identical disclosure.
		{name: "export logs", method: http.MethodGet, path: "/system/logs/export"},
		{name: "get loglevel", method: http.MethodGet, path: "/system/loglevel"},
		{name: "set loglevel", method: http.MethodPut, path: "/system/loglevel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubSensitiveHandler{}
			router := chi.NewRouter()
			registerSystemLogRoutes(router, h)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "user"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d (RequireAdmin should reject role=user)", rec.Code, http.StatusForbidden)
			}
			if len(h.called) != 0 {
				t.Fatalf("handler invoked for non-admin request: %v", h.called)
			}
		})
	}
}

// TestSystemLogRoutesAllowAdmin is the symmetry case: an admin must reach each
// handler (guards against mounting them outside the group so they 404).
func TestSystemLogRoutesAllowAdmin(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		called string
	}{
		{name: "read logs", method: http.MethodGet, path: "/system/logs", called: "list"},
		{name: "export logs", method: http.MethodGet, path: "/system/logs/export", called: "export-logs"},
		{name: "get loglevel", method: http.MethodGet, path: "/system/loglevel", called: "get-level"},
		{name: "set loglevel", method: http.MethodPut, path: "/system/loglevel", called: "set-level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubSensitiveHandler{}
			router := chi.NewRouter()
			registerSystemLogRoutes(router, h)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want %d", rec.Code, http.StatusNoContent)
			}
			if len(h.called) != 1 || h.called[0] != tt.called {
				t.Fatalf("called = %v; want [%s]", h.called, tt.called)
			}
		})
	}
}

// TestOIDCDiscoveryRouteRequiresAdmin locks in #2348: the OIDC discovery probe
// was registered one block above the admin group, so a role=user session could
// POST an arbitrary issuer URL and read back connection refused vs an HTTP
// status vs a parse error vs a full discovery document — a host and port
// oracle for the network the container sits on. An OPDS-only reader account
// authenticates against the same user table, so it reached this too.
func TestOIDCDiscoveryRouteRequiresAdmin(t *testing.T) {
	h := &stubSensitiveHandler{}
	router := chi.NewRouter()
	registerOIDCDiscoveryRoutes(router, h)

	req := httptest.NewRequest(http.MethodPost, "/auth/oidc/test-discovery", strings.NewReader(`{"issuer":"http://192.168.1.1:8080"}`))
	req = req.WithContext(auth.WithUserRole(req.Context(), "user"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d (RequireAdmin should reject role=user)", rec.Code, http.StatusForbidden)
	}
	if len(h.called) != 0 {
		t.Fatalf("handler invoked for non-admin request: %v", h.called)
	}
}

// TestOIDCDiscoveryRouteAllowsAdmin is the symmetry case: gating the probe
// must not take the button away from the admin who configures OIDC.
func TestOIDCDiscoveryRouteAllowsAdmin(t *testing.T) {
	h := &stubSensitiveHandler{}
	router := chi.NewRouter()
	registerOIDCDiscoveryRoutes(router, h)

	req := httptest.NewRequest(http.MethodPost, "/auth/oidc/test-discovery", strings.NewReader(`{"issuer":"https://idp.example.com"}`))
	req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusNoContent)
	}
	if len(h.called) != 1 || h.called[0] != "oidc-test-discovery" {
		t.Fatalf("called = %v; want [oidc-test-discovery]", h.called)
	}
}

// TestSensitiveRoutesRequireAdmin nails down the security finding from the
// v1.15.0 review: List/Get/Create/Update/Delete on the indexer, prowlarr, and
// download-client subtrees must all reject a non-admin caller. The full
// failure mode was that role=user could `GET /indexer` and read every
// Indexer.APIKey out of the response — same for ProwlarrInstance.APIKey and
// DownloadClient.{Username,Password,APIKey}. A future addition that forgets
// to put a new route inside the admin Group will trip this test.
func TestSensitiveRoutesRequireAdmin(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		// Indexer — both reads and mutations are admin-only because the
		// response struct carries APIKey.
		{name: "list indexers", method: http.MethodGet, path: "/indexer"},
		{name: "get indexer", method: http.MethodGet, path: "/indexer/1"},
		{name: "create indexer", method: http.MethodPost, path: "/indexer"},
		{name: "update indexer", method: http.MethodPut, path: "/indexer/1"},
		{name: "delete indexer", method: http.MethodDelete, path: "/indexer/1"},
		{name: "test indexer", method: http.MethodPost, path: "/indexer/1/test"},
		{name: "test indexer config", method: http.MethodPost, path: "/indexer/test"},
		// Prowlarr — entire subtree.
		{name: "list prowlarr", method: http.MethodGet, path: "/prowlarr"},
		{name: "get prowlarr", method: http.MethodGet, path: "/prowlarr/1"},
		{name: "create prowlarr", method: http.MethodPost, path: "/prowlarr"},
		{name: "update prowlarr", method: http.MethodPut, path: "/prowlarr/1"},
		{name: "delete prowlarr", method: http.MethodDelete, path: "/prowlarr/1"},
		{name: "test prowlarr", method: http.MethodPost, path: "/prowlarr/1/test"},
		{name: "sync prowlarr", method: http.MethodPost, path: "/prowlarr/1/sync"},
		// Download clients — both reads and mutations.
		{name: "list download clients", method: http.MethodGet, path: "/downloadclient"},
		{name: "get download client", method: http.MethodGet, path: "/downloadclient/1"},
		{name: "create download client", method: http.MethodPost, path: "/downloadclient"},
		{name: "update download client", method: http.MethodPut, path: "/downloadclient/1"},
		{name: "delete download client", method: http.MethodDelete, path: "/downloadclient/1"},
		{name: "test download client", method: http.MethodPost, path: "/downloadclient/1/test"},
		{name: "test download client config", method: http.MethodPost, path: "/downloadclient/test"},
		// Migrate imports — pull in indexer/client credentials, so admin-only.
		{name: "import csv", method: http.MethodPost, path: "/migrate/csv"},
		{name: "import readarr", method: http.MethodPost, path: "/migrate/readarr"},
		{name: "import readarr status", method: http.MethodGet, path: "/migrate/readarr/status"},
		{name: "import goodreads preview", method: http.MethodPost, path: "/migrate/goodreads/preview"},
		{name: "import goodreads commit", method: http.MethodPost, path: "/migrate/goodreads/commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubSensitiveHandler{}
			router := chi.NewRouter()
			registerIndexerRoutes(router, h)
			registerProwlarrRoutes(router, h)
			registerDownloadClientRoutes(router, h)
			registerMigrateRoutes(router, h)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "user"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d (RequireAdmin should reject role=user)", rec.Code, http.StatusForbidden)
			}
			if len(h.called) != 0 {
				t.Fatalf("handler invoked for non-admin request: %v", h.called)
			}
		})
	}
}

// TestSensitiveRoutesAllowAdmin is the symmetry case: an admin caller must
// reach every handler the previous test asserts is gated. Without this we'd
// risk over-tightening the gate (e.g. accidentally mounting routes outside
// the Group so they 404 instead of dispatching) and not noticing.
func TestSensitiveRoutesAllowAdmin(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		called string
	}{
		{name: "list indexers", method: http.MethodGet, path: "/indexer", called: "list"},
		{name: "get indexer", method: http.MethodGet, path: "/indexer/1", called: "get"},
		{name: "create indexer", method: http.MethodPost, path: "/indexer", called: "create"},
		{name: "update indexer", method: http.MethodPut, path: "/indexer/1", called: "update"},
		{name: "delete indexer", method: http.MethodDelete, path: "/indexer/1", called: "delete"},
		{name: "test indexer", method: http.MethodPost, path: "/indexer/1/test", called: "test"},
		{name: "test indexer config", method: http.MethodPost, path: "/indexer/test", called: "test-config"},
		{name: "list prowlarr", method: http.MethodGet, path: "/prowlarr", called: "list"},
		{name: "get prowlarr", method: http.MethodGet, path: "/prowlarr/1", called: "get"},
		{name: "create prowlarr", method: http.MethodPost, path: "/prowlarr", called: "create"},
		{name: "update prowlarr", method: http.MethodPut, path: "/prowlarr/1", called: "update"},
		{name: "delete prowlarr", method: http.MethodDelete, path: "/prowlarr/1", called: "delete"},
		{name: "test prowlarr", method: http.MethodPost, path: "/prowlarr/1/test", called: "test"},
		{name: "sync prowlarr", method: http.MethodPost, path: "/prowlarr/1/sync", called: "sync"},
		{name: "list download clients", method: http.MethodGet, path: "/downloadclient", called: "list"},
		{name: "get download client", method: http.MethodGet, path: "/downloadclient/1", called: "get"},
		{name: "create download client", method: http.MethodPost, path: "/downloadclient", called: "create"},
		{name: "update download client", method: http.MethodPut, path: "/downloadclient/1", called: "update"},
		{name: "delete download client", method: http.MethodDelete, path: "/downloadclient/1", called: "delete"},
		{name: "test download client", method: http.MethodPost, path: "/downloadclient/1/test", called: "test"},
		{name: "test download client config", method: http.MethodPost, path: "/downloadclient/test", called: "test-config"},
		// Migrate imports — admin must still reach each handler (guards against
		// accidentally mounting them outside the group so they 404 instead).
		{name: "import csv", method: http.MethodPost, path: "/migrate/csv", called: "import-csv"},
		{name: "import readarr", method: http.MethodPost, path: "/migrate/readarr", called: "import-readarr"},
		{name: "import readarr status", method: http.MethodGet, path: "/migrate/readarr/status", called: "import-readarr-status"},
		{name: "import goodreads preview", method: http.MethodPost, path: "/migrate/goodreads/preview", called: "import-goodreads-preview"},
		{name: "import goodreads commit", method: http.MethodPost, path: "/migrate/goodreads/commit", called: "import-goodreads-commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubSensitiveHandler{}
			router := chi.NewRouter()
			registerIndexerRoutes(router, h)
			registerProwlarrRoutes(router, h)
			registerDownloadClientRoutes(router, h)
			registerMigrateRoutes(router, h)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want %d", rec.Code, http.StatusNoContent)
			}
			if len(h.called) != 1 || h.called[0] != tt.called {
				t.Fatalf("called = %v; want [%s]", h.called, tt.called)
			}
		})
	}
}

// TestIndexerPublicReadsAllowNonAdmin keeps a guard on the routes we explicitly
// chose *not* to gate: /indexer/search returns release metadata only (no
// credentials) and /search/last-debug returns the most recent search audit
// trail. If a future refactor mistakenly drops them inside the admin Group,
// non-admin users would lose freeform search.
func TestIndexerPublicReadsAllowNonAdmin(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		called string
	}{
		{name: "indexer search", path: "/indexer/search", called: "search-query"},
		{name: "last search debug", path: "/search/last-debug", called: "last-search-debug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubSensitiveHandler{}
			router := chi.NewRouter()
			registerIndexerRoutes(router, h)

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "user"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want %d (non-admin must reach public reads)", rec.Code, http.StatusNoContent)
			}
			if len(h.called) != 1 || h.called[0] != tt.called {
				t.Fatalf("called = %v; want [%s]", h.called, tt.called)
			}
		})
	}
}

// TestAdminRoutesAnswerInDisabledAuthMode drives admin routes through the
// real auth stack and the real DB backed provider, with the mode read from the
// same setting PUT /auth/mode writes. In disabled mode /auth/status reports
// role admin, so the UI renders the admin screens; before the fix every one
// of them answered 403 "admin role required" because the disabled branch let
// the request through without a role.
func TestAdminRoutesAnswerInDisabledAuthMode(t *testing.T) {
	const apiKey = "route-test-api-key"
	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(conn)
	users := db.NewUserRepo(conn)
	hash, err := auth.HashPassword("route-test-password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := users.Create(ctx, "admin", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, api.SettingAuthAPIKey, apiKey); err != nil {
		t.Fatal(err)
	}
	provider := &dbAuthProvider{settings: settings, users: users}

	dir := t.TempDir()
	storage := api.NewStorageHandler(&config.Config{DownloadDir: dir, LibraryDir: dir})
	logs := &stubSensitiveHandler{}
	var seenID int64
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		useAPIAuth(r, provider)
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenID = auth.UserIDFromContext(r.Context())
				next.ServeHTTP(w, r)
			})
		})
		registerStorageRoutes(r, storage)
		registerSystemLogRoutes(r, logs)
	})

	for _, tc := range []struct {
		name   string
		mode   auth.Mode
		method string
		path   string
		apiKey string
		xrw    bool
		want   int
	}{
		{name: "enabled storage without a cookie", mode: auth.ModeEnabled, method: http.MethodGet, path: "/api/v1/system/storage", want: http.StatusUnauthorized},
		{name: "disabled storage", mode: auth.ModeDisabled, method: http.MethodGet, path: "/api/v1/system/storage", want: http.StatusOK},
		{name: "disabled storage with the api key", mode: auth.ModeDisabled, method: http.MethodGet, path: "/api/v1/system/storage", apiKey: apiKey, want: http.StatusOK},
		{name: "disabled logs", mode: auth.ModeDisabled, method: http.MethodGet, path: "/api/v1/system/logs", want: http.StatusNoContent},
		{name: "disabled log level change from the UI", mode: auth.ModeDisabled, method: http.MethodPut, path: "/api/v1/system/loglevel", xrw: true, want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := settings.Set(ctx, api.SettingAuthMode, string(tc.mode)); err != nil {
				t.Fatal(err)
			}
			seenID = 0
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.apiKey != "" {
				req.Header.Set("X-Api-Key", tc.apiKey)
			}
			if tc.xrw {
				req.Header.Set("X-Requested-With", "bindery-ui")
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want < 300 && seenID != admin.ID {
				t.Errorf("request carried user id %d, want the operator %d", seenID, admin.ID)
			}
		})
	}
}

// pr2361ScanBlob is a library.lastScan value in the shape the scanner writes:
// counts plus the resolved roots and the absolute path of every unmatched
// file. Every path shares one marker so a leak is a substring check.
const pr2361ScanBlob = `{"ran_at":"2026-09-14T10:00:00Z","files_found":3,"reconciled":1,"unmatched":2,` +

	`"library_dir":"/srv/pr2361-root/books","audiobook_dir":"/srv/pr2361-root/audio",` +
	`"scanned_paths":["/srv/pr2361-root/books","/srv/pr2361-root/audio"],` +
	`"unmatched_files":[{"path":"/srv/pr2361-root/books/a.epub","parsed_title":"A","parsed_author":"X"},` +
	`{"path":"/srv/pr2361-root/audio/b","parsed_title":"B","parsed_author":"Y"}]}`

// newScanStatusRouter mounts the production registrar over the real
// LibraryHandler backed by an in-memory settings table holding pr2361ScanBlob,
// so the test exercises the same handler and the same gate as the server.
func newScanStatusRouter(t *testing.T) chi.Router {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(context.Background(), api.SettingLibraryLastScan, pr2361ScanBlob); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	registerLibraryScanStatusRoute(router, api.NewLibraryHandler(nil).WithSettings(settings))
	return router
}

// TestLibraryScanStatusRouteRequiresAdmin is the second door on #2361. #2418
// stopped GET /setting handing library.lastScan to non admins, but GET
// /library/scan/status served the same blob verbatim to every authenticated
// role. A non admin, and a request carrying no role at all, must now be refused
// before the handler runs and must receive none of the paths.
func TestLibraryScanStatusRouteRequiresAdmin(t *testing.T) {
	for _, role := range []string{"user", ""} {
		t.Run("role="+role, func(t *testing.T) {
			router := newScanStatusRouter(t)

			req := httptest.NewRequest(http.MethodGet, "/library/scan/status", nil)
			if role != "" {
				req = req.WithContext(auth.WithUserRole(req.Context(), role))
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d (RequireAdmin should reject role=%q)", rec.Code, http.StatusForbidden, role)
			}
			if body := rec.Body.String(); strings.Contains(body, "/srv/") || strings.Contains(body, "pr2361-root") {
				t.Fatalf("non admin response carries a server path: %s", body)
			}
		})
	}
}

// TestLibraryScanStatusRouteAllowsAdmin is the symmetry case: the Settings
// scan panel is admin UI and needs the whole blob, paths included, so an admin
// must get it back byte for byte.
func TestLibraryScanStatusRouteAllowsAdmin(t *testing.T) {
	router := newScanStatusRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/library/scan/status", nil)
	req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != pr2361ScanBlob {
		t.Fatalf("admin body = %s; want the stored blob unchanged", got)
	}
}
