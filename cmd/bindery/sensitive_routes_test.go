package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
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
func (h *stubSensitiveHandler) Sync(w http.ResponseWriter, _ *http.Request) {
	h.record("sync", w)
}
func (h *stubSensitiveHandler) SearchQuery(w http.ResponseWriter, _ *http.Request) {
	h.record("search-query", w)
}
func (h *stubSensitiveHandler) LastSearchDebug(w http.ResponseWriter, _ *http.Request) {
	h.record("last-search-debug", w)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubSensitiveHandler{}
			router := chi.NewRouter()
			registerIndexerRoutes(router, h)
			registerProwlarrRoutes(router, h)
			registerDownloadClientRoutes(router, h)

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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubSensitiveHandler{}
			router := chi.NewRouter()
			registerIndexerRoutes(router, h)
			registerProwlarrRoutes(router, h)
			registerDownloadClientRoutes(router, h)

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
