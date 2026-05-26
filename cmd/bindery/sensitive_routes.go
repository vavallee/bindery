package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// indexerRouteHandler is the surface registerIndexerRoutes needs. The full
// IndexerHandler exposes more methods (SearchBook, LastSearchDebug,
// resolveAllowedLanguages); only the routes mounted under /indexer are listed
// here so tests can supply a stub.
type indexerRouteHandler interface {
	List(http.ResponseWriter, *http.Request)
	Get(http.ResponseWriter, *http.Request)
	Create(http.ResponseWriter, *http.Request)
	Update(http.ResponseWriter, *http.Request)
	Delete(http.ResponseWriter, *http.Request)
	Test(http.ResponseWriter, *http.Request)
	SearchQuery(http.ResponseWriter, *http.Request)
	LastSearchDebug(http.ResponseWriter, *http.Request)
}

// registerIndexerRoutes mounts the /indexer and /search/last-debug routes.
// List/Get are admin-only because the response includes Indexer.APIKey.
// SearchQuery and LastSearchDebug return only release metadata, so they stay
// open to authenticated non-admin users.
func registerIndexerRoutes(r chi.Router, h indexerRouteHandler) {
	r.Get("/indexer/search", h.SearchQuery)
	r.Get("/search/last-debug", h.LastSearchDebug)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/indexer", h.List)
		r.Get("/indexer/{id}", h.Get)
		r.Post("/indexer", h.Create)
		r.Put("/indexer/{id}", h.Update)
		r.Delete("/indexer/{id}", h.Delete)
		r.Post("/indexer/{id}/test", h.Test)
	})
}

// prowlarrRouteHandler is the surface registerProwlarrRoutes needs.
type prowlarrRouteHandler interface {
	List(http.ResponseWriter, *http.Request)
	Get(http.ResponseWriter, *http.Request)
	Create(http.ResponseWriter, *http.Request)
	Update(http.ResponseWriter, *http.Request)
	Delete(http.ResponseWriter, *http.Request)
	Test(http.ResponseWriter, *http.Request)
	Sync(http.ResponseWriter, *http.Request)
}

// registerProwlarrRoutes mounts /prowlarr/* — the entire subtree is admin-only
// because List/Get return ProwlarrInstance.APIKey and the mutations always were.
func registerProwlarrRoutes(r chi.Router, h prowlarrRouteHandler) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/prowlarr", h.List)
		r.Post("/prowlarr", h.Create)
		r.Get("/prowlarr/{id}", h.Get)
		r.Put("/prowlarr/{id}", h.Update)
		r.Delete("/prowlarr/{id}", h.Delete)
		r.Post("/prowlarr/{id}/test", h.Test)
		r.Post("/prowlarr/{id}/sync", h.Sync)
	})
}

// downloadClientRouteHandler is the surface registerDownloadClientRoutes needs.
type downloadClientRouteHandler interface {
	List(http.ResponseWriter, *http.Request)
	Get(http.ResponseWriter, *http.Request)
	Create(http.ResponseWriter, *http.Request)
	Update(http.ResponseWriter, *http.Request)
	Delete(http.ResponseWriter, *http.Request)
	Test(http.ResponseWriter, *http.Request)
}

// registerDownloadClientRoutes mounts /downloadclient/* — entire subtree is
// admin-only because List/Get return DownloadClient.Username, Password, APIKey.
func registerDownloadClientRoutes(r chi.Router, h downloadClientRouteHandler) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/downloadclient", h.List)
		r.Get("/downloadclient/{id}", h.Get)
		r.Post("/downloadclient", h.Create)
		r.Put("/downloadclient/{id}", h.Update)
		r.Delete("/downloadclient/{id}", h.Delete)
		r.Post("/downloadclient/{id}/test", h.Test)
	})
}
