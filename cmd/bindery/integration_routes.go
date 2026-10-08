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

// registerGrimmoryRoutes mounts /grimmory/*, all admin only, matching the
// abs/* config routes. SetConfig writes the integration URL and credential and
// Test probes it. GetConfig redacts the API key and password but still returns
// the Grimmory base URL and username, the same values GET /setting withholds
// from non admins since #2361, and the only screen that reads it is the admin
// Grimmory tab, so it sits behind the gate too.
func registerGrimmoryRoutes(r chi.Router, h grimmoryRouteHandler) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/grimmory/config", h.GetConfig)
		r.Put("/grimmory/config", h.SetConfig)
		r.Post("/grimmory/test", h.Test)
	})
}

// grimmorySyncRouteHandler is the /grimmory/sync surface.
type grimmorySyncRouteHandler interface {
	Start(http.ResponseWriter, *http.Request)
	Status(http.ResponseWriter, *http.Request)
}

// registerGrimmorySyncRoutes mounts the bulk-push job routes. Admin-only for
// the same reason as the Calibre sync subtree: the job POSTs every imported
// book out to an external service.
func registerGrimmorySyncRoutes(r chi.Router, h grimmorySyncRouteHandler) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Post("/grimmory/sync", h.Start)
		r.Get("/grimmory/sync/status", h.Status)
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
//
// The delivery queue (#2832) is admin only too: its rows carry file paths and
// Calibre's error text, and its actions re-send, drop or forget deliveries.
// The one route outside the group is the per book state, which any user who
// can see the book may read; the handler checks ownership and strips
// everything but the state for non-admins.
func registerCalibreIntegrationRoutes(r chi.Router, probe calibreProbeHandler, imp, sync calibreJobHandler, deliveries calibreDeliveryRouteHandler) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Post("/calibre/test", probe.Test)
		r.Post("/calibre/import", imp.Start)
		r.Get("/calibre/import/status", imp.Status)
		r.Post("/calibre/sync", sync.Start)
		r.Get("/calibre/sync/status", sync.Status)
		r.Get("/calibre/deliveries/summary", deliveries.Summary)
		r.Get("/calibre/deliveries", deliveries.List)
		r.Delete("/calibre/deliveries", deliveries.Clear)
		r.Post("/calibre/deliveries/retry", deliveries.Retry)
		r.Post("/calibre/deliveries/reset", deliveries.Reset)
	})
	r.Get("/book/{id}/calibre", deliveries.BookState)
}

// calibreBridgeRouteHandler is the /bridge/v1 surface (#2833).
type calibreBridgeRouteHandler interface {
	Authenticate(http.Handler) http.Handler
	RequirePull(http.Handler) http.Handler
	Hello(http.ResponseWriter, *http.Request)
	List(http.ResponseWriter, *http.Request)
	File(http.ResponseWriter, *http.Request)
	Cover(http.ResponseWriter, *http.Request)
	Ack(http.ResponseWriter, *http.Request)
	Nack(http.ResponseWriter, *http.Request)
}

// registerCalibreBridgeRoutes mounts /bridge/v1 at the router root, where the
// Calibre plugin pulls its deliveries. Every route needs the plugin API key
// as a Bearer token (Authenticate), in every auth mode. hello answers in
// push too, so the plugin can say Bindery is not in pull mode; the delivery
// routes answer 409 unless Calibre is in plugin mode with the pull
// transport. No session, CSRF or global API key middleware applies: the
// plugin key is the only credential these routes accept.
func registerCalibreBridgeRoutes(r chi.Router, h calibreBridgeRouteHandler) {
	r.Route("/bridge/v1", func(r chi.Router) {
		r.Use(h.Authenticate)
		r.Get("/hello", h.Hello)
		r.Group(func(r chi.Router) {
			r.Use(h.RequirePull)
			r.Get("/deliveries", h.List)
			r.Get("/deliveries/{id}/file", h.File)
			r.Get("/deliveries/{id}/cover", h.Cover)
			r.Post("/deliveries/{id}/ack", h.Ack)
			r.Post("/deliveries/{id}/nack", h.Nack)
		})
	})
}

// calibreDeliveryRouteHandler is the delivery queue surface.
type calibreDeliveryRouteHandler interface {
	Summary(http.ResponseWriter, *http.Request)
	List(http.ResponseWriter, *http.Request)
	Clear(http.ResponseWriter, *http.Request)
	Retry(http.ResponseWriter, *http.Request)
	Reset(http.ResponseWriter, *http.Request)
	BookState(http.ResponseWriter, *http.Request)
}
