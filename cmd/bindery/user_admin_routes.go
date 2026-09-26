package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// userAdminRouteHandler is the surface registerUserAdminRoutes needs.
type userAdminRouteHandler interface {
	List(http.ResponseWriter, *http.Request)
	Create(http.ResponseWriter, *http.Request)
	Delete(http.ResponseWriter, *http.Request)
	SetRole(http.ResponseWriter, *http.Request)
	SetAutoApprove(http.ResponseWriter, *http.Request)
	ResetPassword(http.ResponseWriter, *http.Request)
}

// registerUserAdminRoutes mounts the admin user management API. The whole
// subtree is admin-only: it creates accounts, changes roles, resets passwords
// and can make an account's requests approve themselves with no human in the
// loop. The RequireAdmin group lives here rather than at the call site so the
// route-level test in user_admin_routes_test.go exercises the same registration
// the server uses.
func registerUserAdminRoutes(r chi.Router, h userAdminRouteHandler) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/auth/users", h.List)
		r.Post("/auth/users", h.Create)
		r.Delete("/auth/users/{id}", h.Delete)
		r.Put("/auth/users/{id}/role", h.SetRole)
		r.Put("/auth/users/{id}/auto-approve", h.SetAutoApprove)
		r.Put("/auth/users/{id}/reset-password", h.ResetPassword)
	})
}
