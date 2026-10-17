package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/covers"
)

type bookCoverRouteHandler interface {
	UploadCover(http.ResponseWriter, *http.Request)
}

// registerBookCoverRoutes mounts the cover upload route.
func registerBookCoverRoutes(r chi.Router, h bookCoverRouteHandler) {
	// The router wide 1 MiB cap would refuse most real covers, so the route
	// raises it to what the handler reads: one byte past the cover limit lets
	// it tell an oversize cover from one exactly at the limit.
	r.With(api.WithMaxBody(covers.MaxBytes+1)).Post("/book/{id}/cover", h.UploadCover)
}

type authorPhotoRouteHandler interface {
	UploadPhoto(http.ResponseWriter, *http.Request)
}

// registerAuthorPhotoRoutes mounts the author photo upload route, with the
// same raised body cap as the cover upload (#3287).
func registerAuthorPhotoRoutes(r chi.Router, h authorPhotoRouteHandler) {
	r.With(api.WithMaxBody(covers.MaxBytes+1)).Post("/author/{id}/photo", h.UploadPhoto)
}
