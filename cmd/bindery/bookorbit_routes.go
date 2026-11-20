package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// bookOrbitRouteHandler is the surface registerBookOrbitRoutes needs.
type bookOrbitRouteHandler interface {
	GetConfig(http.ResponseWriter, *http.Request)
	SetConfig(http.ResponseWriter, *http.Request)
	Test(http.ResponseWriter, *http.Request)
	Libraries(http.ResponseWriter, *http.Request)
	StartImport(http.ResponseWriter, *http.Request)
	ImportStatus(http.ResponseWriter, *http.Request)
	ImportRuns(http.ResponseWriter, *http.Request)
}

// registerBookOrbitRoutes mounts /bookorbit/*, all admin only, matching the
// abs/* routes. SetConfig stores a server URL and an API key, Test and
// Libraries make Bindery call that server, and the import creates authors and
// books for the whole install. The reads sit behind the gate as well: the
// config names an internal address, and status and run history carry book
// titles and file paths from every library.
func registerBookOrbitRoutes(r chi.Router, h bookOrbitRouteHandler) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/bookorbit/config", h.GetConfig)
		r.Put("/bookorbit/config", h.SetConfig)
		r.Post("/bookorbit/test", h.Test)
		r.Post("/bookorbit/libraries", h.Libraries)
		r.Post("/bookorbit/import", h.StartImport)
		r.Get("/bookorbit/import/status", h.ImportStatus)
		r.Get("/bookorbit/import/runs", h.ImportRuns)
	})
}
