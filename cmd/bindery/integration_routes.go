package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// These helpers mount the root-folder, Grimmory, and Calibre-integration routes
// behind the same admin-gate boundary the credential-bearing routes use (see
// sensitive_routes.go). Keeping them as small, standalone registration
// functions makes the boundary a single testable shape — integration_routes_test
// asserts a non-admin caller is rejected and an admin still reaches each handler.

// rootFolderRouteHandler is the surface registerRootFolderRoutes needs.
type rootFolderRouteHandler interface {
	List(http.ResponseWriter, *http.Request)
	Create(http.ResponseWriter, *http.Request)
	Delete(http.ResponseWriter, *http.Request)
}

// registerRootFolderRoutes mounts /rootfolder. List (reads) stays open, but
// Create and Delete register/remove server filesystem storage roots — global
// infrastructure config — so they are admin-only.
func registerRootFolderRoutes(r chi.Router, h rootFolderRouteHandler) {
	r.Get("/rootfolder", h.List)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Post("/rootfolder", h.Create)
		r.Delete("/rootfolder/{id}", h.Delete)
	})
}

// grimmoryRouteHandler is the surface registerGrimmoryRoutes needs.
type grimmoryRouteHandler interface {
	GetConfig(http.ResponseWriter, *http.Request)
	SetConfig(http.ResponseWriter, *http.Request)
	Test(http.ResponseWriter, *http.Request)
}

// registerGrimmoryRoutes mounts /grimmory/*. GetConfig redacts the API key so
// it stays open; SetConfig writes the integration URL + credential and Test
// probes it, so those are admin-only (matching the abs/* config pattern).
func registerGrimmoryRoutes(r chi.Router, h grimmoryRouteHandler) {
	r.Get("/grimmory/config", h.GetConfig)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Put("/grimmory/config", h.SetConfig)
		r.Post("/grimmory/test", h.Test)
	})
}

// calibreProbeHandler is the /calibre/test surface.
type calibreProbeHandler interface {
	Test(http.ResponseWriter, *http.Request)
}

// calibreJobHandler is the Start/Status surface shared by the import and sync
// handlers.
type calibreJobHandler interface {
	Start(http.ResponseWriter, *http.Request)
	Status(http.ResponseWriter, *http.Request)
}

// registerCalibreIntegrationRoutes mounts the Calibre probe/import/sync routes.
// The whole subtree is admin-only: probing runs a credentialed connection test,
// import creates authors/books wholesale (like /migrate), and sync POSTs every
// imported book out to the external plugin. The status polls are grouped in for
// consistency since only an admin can start the jobs, and to match the
// abs/import and calibre-rollback boundaries.
func registerCalibreIntegrationRoutes(r chi.Router, probe calibreProbeHandler, imp, sync calibreJobHandler) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Post("/calibre/test", probe.Test)
		r.Post("/calibre/import", imp.Start)
		r.Get("/calibre/import/status", imp.Status)
		r.Post("/calibre/sync", sync.Start)
		r.Get("/calibre/sync/status", sync.Status)
	})
}
