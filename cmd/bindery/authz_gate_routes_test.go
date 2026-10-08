package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// stubAuthzGateHandler stands in for the blocklist and metadata profile
// handlers. Each method records its name and writes 204, so a 403 with an
// empty `called` slice means RequireAdmin stopped the request first.
type stubAuthzGateHandler struct {
	called []string
}

func (h *stubAuthzGateHandler) record(name string, w http.ResponseWriter) {
	h.called = append(h.called, name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubAuthzGateHandler) List(w http.ResponseWriter, _ *http.Request) { h.record("list", w) }
func (h *stubAuthzGateHandler) Get(w http.ResponseWriter, _ *http.Request)  { h.record("get", w) }
func (h *stubAuthzGateHandler) Create(w http.ResponseWriter, _ *http.Request) {
	h.record("create", w)
}
func (h *stubAuthzGateHandler) Update(w http.ResponseWriter, _ *http.Request) {
	h.record("update", w)
}
func (h *stubAuthzGateHandler) Delete(w http.ResponseWriter, _ *http.Request) {
	h.record("delete", w)
}
func (h *stubAuthzGateHandler) BulkDelete(w http.ResponseWriter, _ *http.Request) {
	h.record("bulk-delete", w)
}
func (h *stubAuthzGateHandler) FilteredBooks(w http.ResponseWriter, _ *http.Request) {
	h.record("filtered-books", w)
}
func (h *stubAuthzGateHandler) UnmonitorFilteredBooks(w http.ResponseWriter, _ *http.Request) {
	h.record("unmonitor-filtered-books", w)
}

// metadataProfileLibraryRoutes apply a profile to books already stored
// (#2208). The preview lists wanted books across every library and the
// action changes them, so both are admin only.
var metadataProfileLibraryRoutes = []authzGateRoute{
	{name: "preview filtered books", method: http.MethodGet, path: "/metadataprofile/3/filtered-books", called: "filtered-books"},
	{name: "unmonitor filtered books", method: http.MethodPost, path: "/metadataprofile/3/filtered-books/unmonitor", called: "unmonitor-filtered-books"},
}

func TestMetadataProfileLibraryRoutesRequireAdmin(t *testing.T) {
	register := func(r chi.Router, h *stubAuthzGateHandler) { registerMetadataProfileLibraryRoutes(r, h) }
	for _, rt := range metadataProfileLibraryRoutes {
		t.Run("user "+rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, register, rt, "user")
			if rec.Code != http.StatusForbidden || len(h.called) != 0 {
				t.Fatalf("status = %d called = %v; want 403 and no handler call", rec.Code, h.called)
			}
		})
		t.Run("admin "+rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, register, rt, "admin")
			if rec.Code != http.StatusNoContent || len(h.called) != 1 || h.called[0] != rt.called {
				t.Fatalf("status = %d called = %v; want 204 [%s]", rec.Code, h.called, rt.called)
			}
		})
	}
}

type authzGateRoute struct {
	name   string
	method string
	path   string
	called string
}

// blocklistRoutes is every /blocklist route. The blocklist is app wide (no
// owner column), so a role=user caller who could list it saw every release an
// admin had blocked, and one who could delete from it re-enabled them.
var blocklistRoutes = []authzGateRoute{
	{name: "list blocklist", method: http.MethodGet, path: "/blocklist", called: "list"},
	{name: "bulk delete blocklist", method: http.MethodDelete, path: "/blocklist/bulk", called: "bulk-delete"},
	{name: "delete blocklist entry", method: http.MethodDelete, path: "/blocklist/7", called: "delete"},
}

// metadataProfileWriteRoutes are the profile mutations, which must match the
// quality profile gating: a profile filters what every author refresh adds.
var metadataProfileWriteRoutes = []authzGateRoute{
	{name: "create metadata profile", method: http.MethodPost, path: "/metadataprofile", called: "create"},
	{name: "update metadata profile", method: http.MethodPut, path: "/metadataprofile/3", called: "update"},
	{name: "delete metadata profile", method: http.MethodDelete, path: "/metadataprofile/3", called: "delete"},
}

// metadataProfileReadRoutes stay open: the author add and edit forms read
// the profile list to fill their picker for every user.
var metadataProfileReadRoutes = []authzGateRoute{
	{name: "list metadata profiles", method: http.MethodGet, path: "/metadataprofile", called: "list"},
	{name: "get metadata profile", method: http.MethodGet, path: "/metadataprofile/3", called: "get"},
}

func serveAuthzGateRoute(t *testing.T, register func(chi.Router, *stubAuthzGateHandler), rt authzGateRoute, role string) (*httptest.ResponseRecorder, *stubAuthzGateHandler) {
	t.Helper()
	h := &stubAuthzGateHandler{}
	router := chi.NewRouter()
	register(router, h)
	var body *strings.Reader
	if rt.method == http.MethodGet {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(`{}`)
	}
	req := httptest.NewRequest(rt.method, rt.path, body)
	req = req.WithContext(auth.WithUserRole(req.Context(), role))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec, h
}

func registerBlocklistStub(r chi.Router, h *stubAuthzGateHandler) { registerBlocklistRoutes(r, h) }
func registerMetadataProfileStub(r chi.Router, h *stubAuthzGateHandler) {
	registerMetadataProfileRoutes(r, h)
}
func registerQualityProfileStub(r chi.Router, h *stubAuthzGateHandler) {
	registerQualityProfileRoutes(r, h)
}

var qualityProfileWriteRoutes = []authzGateRoute{
	{name: "create quality profile", method: http.MethodPost, path: "/qualityprofile", called: "create"},
	{name: "update quality profile", method: http.MethodPut, path: "/qualityprofile/3", called: "update"},
	{name: "delete quality profile", method: http.MethodDelete, path: "/qualityprofile/3", called: "delete"},
}

var qualityProfileReadRoutes = []authzGateRoute{
	{name: "list quality profiles", method: http.MethodGet, path: "/qualityprofile", called: "list"},
	{name: "get quality profile", method: http.MethodGet, path: "/qualityprofile/3", called: "get"},
}

// TestQualityProfileRoutesReadsOpenWritesAdmin pins the quality profile split
// that metadata profiles now share: role=user reads, gets 403 on every write,
// and an admin reaches everything.
func TestQualityProfileRoutesReadsOpenWritesAdmin(t *testing.T) {
	for _, rt := range qualityProfileReadRoutes {
		t.Run("user "+rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, registerQualityProfileStub, rt, "user")
			if rec.Code != http.StatusNoContent || len(h.called) != 1 || h.called[0] != rt.called {
				t.Fatalf("status = %d called = %v; want 204 [%s]", rec.Code, h.called, rt.called)
			}
		})
	}
	for _, rt := range qualityProfileWriteRoutes {
		t.Run("user "+rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, registerQualityProfileStub, rt, "user")
			if rec.Code != http.StatusForbidden || len(h.called) != 0 {
				t.Fatalf("status = %d called = %v; want 403 and no handler call", rec.Code, h.called)
			}
		})
	}
	all := append(append([]authzGateRoute{}, qualityProfileReadRoutes...), qualityProfileWriteRoutes...)
	for _, rt := range all {
		t.Run("admin "+rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, registerQualityProfileStub, rt, "admin")
			if rec.Code != http.StatusNoContent || len(h.called) != 1 || h.called[0] != rt.called {
				t.Fatalf("status = %d called = %v; want 204 [%s]", rec.Code, h.called, rt.called)
			}
		})
	}
}

// TestBlocklistRoutesRequireAdmin: docs/multi-user.md lists Blocklist under the
// admin only System tab, but the routes sat outside any admin group.
func TestBlocklistRoutesRequireAdmin(t *testing.T) {
	for _, rt := range blocklistRoutes {
		t.Run(rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, registerBlocklistStub, rt, "user")
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d (RequireAdmin should reject role=user)", rec.Code, http.StatusForbidden)
			}
			if len(h.called) != 0 {
				t.Fatalf("handler invoked for non-admin request: %v", h.called)
			}
		})
	}
}

func TestBlocklistRoutesAllowAdmin(t *testing.T) {
	for _, rt := range blocklistRoutes {
		t.Run(rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, registerBlocklistStub, rt, "admin")
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want %d", rec.Code, http.StatusNoContent)
			}
			if len(h.called) != 1 || h.called[0] != rt.called {
				t.Fatalf("called = %v; want [%s]", h.called, rt.called)
			}
		})
	}
}

// TestMetadataProfileWritesRequireAdmin: quality profile writes were admin
// only while metadata profile writes were open to role=user.
func TestMetadataProfileWritesRequireAdmin(t *testing.T) {
	for _, rt := range metadataProfileWriteRoutes {
		t.Run(rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, registerMetadataProfileStub, rt, "user")
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d (RequireAdmin should reject role=user)", rec.Code, http.StatusForbidden)
			}
			if len(h.called) != 0 {
				t.Fatalf("handler invoked for non-admin request: %v", h.called)
			}
		})
	}
}

// TestMetadataProfileRoutesReadsOpenWritesAdmin is the symmetry case: the
// reads stay reachable for role=user, and an admin reaches every route.
func TestMetadataProfileRoutesReadsOpenWritesAdmin(t *testing.T) {
	for _, rt := range metadataProfileReadRoutes {
		t.Run("user "+rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, registerMetadataProfileStub, rt, "user")
			if rec.Code != http.StatusNoContent || len(h.called) != 1 || h.called[0] != rt.called {
				t.Fatalf("status = %d called = %v; want 204 [%s]", rec.Code, h.called, rt.called)
			}
		})
	}
	all := append(append([]authzGateRoute{}, metadataProfileReadRoutes...), metadataProfileWriteRoutes...)
	for _, rt := range all {
		t.Run("admin "+rt.name, func(t *testing.T) {
			rec, h := serveAuthzGateRoute(t, registerMetadataProfileStub, rt, "admin")
			if rec.Code != http.StatusNoContent || len(h.called) != 1 || h.called[0] != rt.called {
				t.Fatalf("status = %d called = %v; want 204 [%s]", rec.Code, h.called, rt.called)
			}
		})
	}
}
