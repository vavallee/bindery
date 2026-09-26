package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// stubUserAdminHandler records which handler method a request reached. If
// RequireAdmin rejects the caller the handler never runs, so an empty `called`
// slice with 403 is the gated path.
type stubUserAdminHandler struct {
	called []string
}

func (h *stubUserAdminHandler) record(name string, w http.ResponseWriter) {
	h.called = append(h.called, name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubUserAdminHandler) List(w http.ResponseWriter, _ *http.Request)   { h.record("list", w) }
func (h *stubUserAdminHandler) Create(w http.ResponseWriter, _ *http.Request) { h.record("create", w) }
func (h *stubUserAdminHandler) Delete(w http.ResponseWriter, _ *http.Request) { h.record("delete", w) }
func (h *stubUserAdminHandler) SetRole(w http.ResponseWriter, _ *http.Request) {
	h.record("set-role", w)
}
func (h *stubUserAdminHandler) SetAutoApprove(w http.ResponseWriter, _ *http.Request) {
	h.record("set-auto-approve", w)
}
func (h *stubUserAdminHandler) ResetPassword(w http.ResponseWriter, _ *http.Request) {
	h.record("reset-password", w)
}

// TestUserAdminRoutesRequireAdmin pins the gate on the admin user API. Moving
// PUT /auth/users/{id}/auto-approve out of the RequireAdmin group left the
// suite green before this test, even though the flag lets an account's
// requests add themselves with no human in the loop (#2718). It covers the
// neighbouring user routes for the same reason: they set roles and reset
// passwords, and none of them had a route-level test either.
func TestUserAdminRoutesRequireAdmin(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "list users", method: http.MethodGet, path: "/auth/users"},
		{name: "create user", method: http.MethodPost, path: "/auth/users"},
		{name: "delete user", method: http.MethodDelete, path: "/auth/users/2"},
		{name: "set role", method: http.MethodPut, path: "/auth/users/2/role"},
		{name: "set auto approve", method: http.MethodPut, path: "/auth/users/2/auto-approve"},
		{name: "reset password", method: http.MethodPut, path: "/auth/users/2/reset-password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubUserAdminHandler{}
			router := chi.NewRouter()
			registerUserAdminRoutes(router, h)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), auth.RoleUser))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d (RequireAdmin should reject role=user)", rec.Code, http.StatusForbidden)
			}
			if len(h.called) != 0 {
				t.Fatalf("handler invoked for a non-admin request: %v", h.called)
			}
		})
	}
}

// TestUserAdminRoutesAllowAdmin is the symmetry case: an admin must reach each
// handler, so the gate is not accidentally widened to 404.
func TestUserAdminRoutesAllowAdmin(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		called string
	}{
		{name: "list users", method: http.MethodGet, path: "/auth/users", called: "list"},
		{name: "create user", method: http.MethodPost, path: "/auth/users", called: "create"},
		{name: "delete user", method: http.MethodDelete, path: "/auth/users/2", called: "delete"},
		{name: "set role", method: http.MethodPut, path: "/auth/users/2/role", called: "set-role"},
		{name: "set auto approve", method: http.MethodPut, path: "/auth/users/2/auto-approve", called: "set-auto-approve"},
		{name: "reset password", method: http.MethodPut, path: "/auth/users/2/reset-password", called: "reset-password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &stubUserAdminHandler{}
			router := chi.NewRouter()
			registerUserAdminRoutes(router, h)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), auth.RoleAdmin))
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
