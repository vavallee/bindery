package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

type stubBookOrbitHandler struct {
	called []string
}

func (h *stubBookOrbitHandler) record(name string, w http.ResponseWriter) {
	h.called = append(h.called, name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubBookOrbitHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	h.record("get-config", w)
}
func (h *stubBookOrbitHandler) SetConfig(w http.ResponseWriter, _ *http.Request) {
	h.record("set-config", w)
}
func (h *stubBookOrbitHandler) Test(w http.ResponseWriter, _ *http.Request) { h.record("test", w) }
func (h *stubBookOrbitHandler) Libraries(w http.ResponseWriter, _ *http.Request) {
	h.record("libraries", w)
}
func (h *stubBookOrbitHandler) StartImport(w http.ResponseWriter, _ *http.Request) {
	h.record("start", w)
}
func (h *stubBookOrbitHandler) ImportStatus(w http.ResponseWriter, _ *http.Request) {
	h.record("status", w)
}
func (h *stubBookOrbitHandler) ImportRuns(w http.ResponseWriter, _ *http.Request) {
	h.record("runs", w)
}

var bookOrbitRoutes = []struct {
	method string
	path   string
	called string
}{
	{http.MethodGet, "/bookorbit/config", "get-config"},
	{http.MethodPut, "/bookorbit/config", "set-config"},
	{http.MethodPost, "/bookorbit/test", "test"},
	{http.MethodPost, "/bookorbit/libraries", "libraries"},
	{http.MethodPost, "/bookorbit/import", "start"},
	{http.MethodGet, "/bookorbit/import/status", "status"},
	{http.MethodGet, "/bookorbit/import/runs", "runs"},
}

// TestBookOrbitRoutesRequireAdmin: every BookOrbit route, reads included,
// answers 403 to a non admin and never reaches its handler. The config holds
// an internal address, and the status and run history carry titles and file
// paths from the whole install.
func TestBookOrbitRoutesRequireAdmin(t *testing.T) {
	for _, role := range []string{"user", auth.RoleRequester, ""} {
		for _, rt := range bookOrbitRoutes {
			t.Run(role+" "+rt.method+" "+rt.path, func(t *testing.T) {
				h := &stubBookOrbitHandler{}
				router := chi.NewRouter()
				registerBookOrbitRoutes(router, h)
				req := httptest.NewRequest(rt.method, rt.path, nil)
				req = req.WithContext(auth.WithUserRole(req.Context(), role))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403 for role %q", rec.Code, role)
				}
				if len(h.called) != 0 {
					t.Fatalf("handler reached for role %q: %v", role, h.called)
				}
			})
		}
	}
}

// TestBookOrbitRoutesAllowAdmin guards against a route mounted where an admin
// cannot reach it.
func TestBookOrbitRoutesAllowAdmin(t *testing.T) {
	for _, rt := range bookOrbitRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			h := &stubBookOrbitHandler{}
			router := chi.NewRouter()
			registerBookOrbitRoutes(router, h)
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent || len(h.called) != 1 || h.called[0] != rt.called {
				t.Fatalf("status = %d, called = %v; want 204 and [%s]", rec.Code, h.called, rt.called)
			}
		})
	}
}
