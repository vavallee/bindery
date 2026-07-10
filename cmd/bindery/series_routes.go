package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

type seriesRouteHandler interface {
	List(http.ResponseWriter, *http.Request)
	Create(http.ResponseWriter, *http.Request)
	SearchHardcover(http.ResponseWriter, *http.Request)
	Get(http.ResponseWriter, *http.Request)
	Update(http.ResponseWriter, *http.Request)
	Monitor(http.ResponseWriter, *http.Request)
	Delete(http.ResponseWriter, *http.Request)
	AddBook(http.ResponseWriter, *http.Request)
	Fill(http.ResponseWriter, *http.Request)
	ApplyGenres(http.ResponseWriter, *http.Request)
	GetHardcoverLink(http.ResponseWriter, *http.Request)
	AutoLinkHardcover(http.ResponseWriter, *http.Request)
	PutHardcoverLink(http.ResponseWriter, *http.Request)
	DeleteHardcoverLink(http.ResponseWriter, *http.Request)
	HardcoverDiff(http.ResponseWriter, *http.Request)
}

func registerSeriesRoutes(r chi.Router, seriesHandler seriesRouteHandler) {
	r.Get("/series", seriesHandler.List)
	r.Get("/series/hardcover/search", seriesHandler.SearchHardcover)
	r.Get("/series/{id}", seriesHandler.Get)
	r.Get("/series/{id}/hardcover-link", seriesHandler.GetHardcoverLink)
	r.Get("/series/{id}/hardcover-diff", seriesHandler.HardcoverDiff)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Post("/series", seriesHandler.Create)
		r.Put("/series/{id}", seriesHandler.Update)
		r.Patch("/series/{id}", seriesHandler.Monitor)
		r.Delete("/series/{id}", seriesHandler.Delete)
		r.Post("/series/{id}/books", seriesHandler.AddBook)
		r.Post("/series/{id}/fill", seriesHandler.Fill)
		r.Put("/series/{id}/genres", seriesHandler.ApplyGenres)
		r.Post("/series/{id}/hardcover-link/auto", seriesHandler.AutoLinkHardcover)
		r.Put("/series/{id}/hardcover-link", seriesHandler.PutHardcoverLink)
		r.Delete("/series/{id}/hardcover-link", seriesHandler.DeleteHardcoverLink)
	})
}
