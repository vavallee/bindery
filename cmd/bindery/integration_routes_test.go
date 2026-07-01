package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// stubIntegrationHandler stands in for the root-folder, Grimmory, and Calibre
// handlers. Every method records its name and writes 204, so when RequireAdmin
// rejects a request the handler never runs and `called` stays empty.
type stubIntegrationHandler struct {
	called []string
}

func (h *stubIntegrationHandler) record(name string, w http.ResponseWriter) {
	h.called = append(h.called, name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubIntegrationHandler) List(w http.ResponseWriter, _ *http.Request) { h.record("list", w) }
func (h *stubIntegrationHandler) Create(w http.ResponseWriter, _ *http.Request) {
	h.record("create", w)
}
func (h *stubIntegrationHandler) Delete(w http.ResponseWriter, _ *http.Request) {
	h.record("delete", w)
}
func (h *stubIntegrationHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	h.record("get-config", w)
}
func (h *stubIntegrationHandler) SetConfig(w http.ResponseWriter, _ *http.Request) {
	h.record("set-config", w)
}
func (h *stubIntegrationHandler) Test(w http.ResponseWriter, _ *http.Request)  { h.record("test", w) }
func (h *stubIntegrationHandler) Start(w http.ResponseWriter, _ *http.Request) { h.record("start", w) }
func (h *stubIntegrationHandler) Status(w http.ResponseWriter, _ *http.Request) {
	h.record("status", w)
}

// newIntegrationRouter wires the three integration route helpers onto a fresh
// router with one shared stub, mirroring how main.go mounts them.
func newIntegrationRouter(h *stubIntegrationHandler) chi.Router {
	router := chi.NewRouter()
	registerRootFolderRoutes(router, h)
	registerGrimmoryRoutes(router, h)
	registerCalibreIntegrationRoutes(router, h, h, h)
	return router
}

// TestIntegrationRoutesRequireAdmin nails down the audit finding that a
// non-admin session could register/delete storage roots, rewrite the Grimmory
// integration credential, and drive the Calibre import/sync (bulk DB writes +
// pushing the whole library to an external plugin). Each gated route must 403
// for role=user.
func TestIntegrationRoutesRequireAdmin(t *testing.T) {
	gated := []struct {
		name   string
		method string
		path   string
	}{
		{"create root folder", http.MethodPost, "/rootfolder"},
		{"delete root folder", http.MethodDelete, "/rootfolder/1"},
		{"set grimmory config", http.MethodPut, "/grimmory/config"},
		{"test grimmory", http.MethodPost, "/grimmory/test"},
		{"test calibre", http.MethodPost, "/calibre/test"},
		{"start calibre import", http.MethodPost, "/calibre/import"},
		{"calibre import status", http.MethodGet, "/calibre/import/status"},
		{"start calibre sync", http.MethodPost, "/calibre/sync"},
		{"calibre sync status", http.MethodGet, "/calibre/sync/status"},
	}
	for _, tt := range gated {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubIntegrationHandler{}
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "user"))
			rec := httptest.NewRecorder()
			newIntegrationRouter(h).ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d (RequireAdmin should reject role=user)", rec.Code, http.StatusForbidden)
			}
			if len(h.called) != 0 {
				t.Fatalf("handler invoked for non-admin request: %v", h.called)
			}
		})
	}
}

// TestIntegrationRoutesAllowAdmin is the symmetry case: an admin must still
// reach every gated handler (guards against mounting a route outside the group
// so it 404s instead of dispatching).
func TestIntegrationRoutesAllowAdmin(t *testing.T) {
	tests := []struct {
		method string
		path   string
		called string
	}{
		{http.MethodPost, "/rootfolder", "create"},
		{http.MethodDelete, "/rootfolder/1", "delete"},
		{http.MethodPut, "/grimmory/config", "set-config"},
		{http.MethodPost, "/grimmory/test", "test"},
		{http.MethodPost, "/calibre/test", "test"},
		{http.MethodPost, "/calibre/import", "start"},
		{http.MethodGet, "/calibre/import/status", "status"},
		{http.MethodPost, "/calibre/sync", "start"},
		{http.MethodGet, "/calibre/sync/status", "status"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			h := &stubIntegrationHandler{}
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
			rec := httptest.NewRecorder()
			newIntegrationRouter(h).ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want %d", rec.Code, http.StatusNoContent)
			}
			if len(h.called) != 1 || h.called[0] != tt.called {
				t.Fatalf("called = %v; want [%s]", h.called, tt.called)
			}
		})
	}
}

// TestIntegrationOpenReadsAllowNonAdmin guards the reads we deliberately kept
// open: GET /rootfolder and GET /grimmory/config (which redacts its key). If a
// refactor drops them inside the admin group, non-admin UI loses them.
func TestIntegrationOpenReadsAllowNonAdmin(t *testing.T) {
	tests := []struct {
		path   string
		called string
	}{
		{"/rootfolder", "list"},
		{"/grimmory/config", "get-config"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			h := &stubIntegrationHandler{}
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "user"))
			rec := httptest.NewRecorder()
			newIntegrationRouter(h).ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want %d (non-admin must reach open reads)", rec.Code, http.StatusNoContent)
			}
			if len(h.called) != 1 || h.called[0] != tt.called {
				t.Fatalf("called = %v; want [%s]", h.called, tt.called)
			}
		})
	}
}
