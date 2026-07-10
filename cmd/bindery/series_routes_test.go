package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

type stubSeriesRouteHandler struct {
	called []string
}

func (h *stubSeriesRouteHandler) record(name string, w http.ResponseWriter) {
	h.called = append(h.called, name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubSeriesRouteHandler) List(w http.ResponseWriter, _ *http.Request) {
	h.record("list", w)
}

func (h *stubSeriesRouteHandler) Create(w http.ResponseWriter, _ *http.Request) {
	h.record("create", w)
}

func (h *stubSeriesRouteHandler) SearchHardcover(w http.ResponseWriter, _ *http.Request) {
	h.record("search-hardcover", w)
}

func (h *stubSeriesRouteHandler) Get(w http.ResponseWriter, _ *http.Request) {
	h.record("get", w)
}

func (h *stubSeriesRouteHandler) Update(w http.ResponseWriter, _ *http.Request) {
	h.record("update", w)
}

func (h *stubSeriesRouteHandler) Monitor(w http.ResponseWriter, _ *http.Request) {
	h.record("monitor", w)
}

func (h *stubSeriesRouteHandler) Delete(w http.ResponseWriter, _ *http.Request) {
	h.record("delete", w)
}

func (h *stubSeriesRouteHandler) AddBook(w http.ResponseWriter, _ *http.Request) {
	h.record("add-book", w)
}

func (h *stubSeriesRouteHandler) Fill(w http.ResponseWriter, _ *http.Request) {
	h.record("fill", w)
}

func (h *stubSeriesRouteHandler) ApplyGenres(w http.ResponseWriter, _ *http.Request) {
	h.record("applyGenres", w)
}

func (h *stubSeriesRouteHandler) GetHardcoverLink(w http.ResponseWriter, _ *http.Request) {
	h.record("get-hardcover-link", w)
}

func (h *stubSeriesRouteHandler) AutoLinkHardcover(w http.ResponseWriter, _ *http.Request) {
	h.record("auto-link-hardcover", w)
}

func (h *stubSeriesRouteHandler) PutHardcoverLink(w http.ResponseWriter, _ *http.Request) {
	h.record("put-hardcover-link", w)
}

func (h *stubSeriesRouteHandler) DeleteHardcoverLink(w http.ResponseWriter, _ *http.Request) {
	h.record("delete-hardcover-link", w)
}

func (h *stubSeriesRouteHandler) HardcoverDiff(w http.ResponseWriter, _ *http.Request) {
	h.record("hardcover-diff", w)
}

func TestSeriesMutationRoutesRequireAdmin(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create series", method: http.MethodPost, path: "/series"},
		{name: "update series", method: http.MethodPut, path: "/series/1"},
		{name: "monitor series", method: http.MethodPatch, path: "/series/1"},
		{name: "delete series", method: http.MethodDelete, path: "/series/1"},
		{name: "add book", method: http.MethodPost, path: "/series/1/books"},
		{name: "fill series", method: http.MethodPost, path: "/series/1/fill"},
		{name: "auto link hardcover", method: http.MethodPost, path: "/series/1/hardcover-link/auto"},
		{name: "put hardcover link", method: http.MethodPut, path: "/series/1/hardcover-link"},
		{name: "delete hardcover link", method: http.MethodDelete, path: "/series/1/hardcover-link"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &stubSeriesRouteHandler{}
			router := chi.NewRouter()
			registerSeriesRoutes(router, handler)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "user"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d", rec.Code, http.StatusForbidden)
			}
			if len(handler.called) != 0 {
				t.Fatalf("handler called for non-admin request: %v", handler.called)
			}
		})
	}
}

func TestSeriesMutationRoutesAllowAdmin(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		called string
	}{
		{name: "create series", method: http.MethodPost, path: "/series", called: "create"},
		{name: "update series", method: http.MethodPut, path: "/series/1", called: "update"},
		{name: "monitor series", method: http.MethodPatch, path: "/series/1", called: "monitor"},
		{name: "delete series", method: http.MethodDelete, path: "/series/1", called: "delete"},
		{name: "add book", method: http.MethodPost, path: "/series/1/books", called: "add-book"},
		{name: "fill series", method: http.MethodPost, path: "/series/1/fill", called: "fill"},
		{name: "auto link hardcover", method: http.MethodPost, path: "/series/1/hardcover-link/auto", called: "auto-link-hardcover"},
		{name: "put hardcover link", method: http.MethodPut, path: "/series/1/hardcover-link", called: "put-hardcover-link"},
		{name: "delete hardcover link", method: http.MethodDelete, path: "/series/1/hardcover-link", called: "delete-hardcover-link"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &stubSeriesRouteHandler{}
			router := chi.NewRouter()
			registerSeriesRoutes(router, handler)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want %d", rec.Code, http.StatusNoContent)
			}
			if len(handler.called) != 1 || handler.called[0] != tt.called {
				t.Fatalf("called = %v; want [%s]", handler.called, tt.called)
			}
		})
	}
}

func TestSeriesReadRoutesAllowNonAdmin(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		called string
	}{
		{name: "list series", path: "/series", called: "list"},
		{name: "search hardcover", path: "/series/hardcover/search", called: "search-hardcover"},
		{name: "get series", path: "/series/1", called: "get"},
		{name: "get hardcover link", path: "/series/1/hardcover-link", called: "get-hardcover-link"},
		{name: "hardcover diff", path: "/series/1/hardcover-diff", called: "hardcover-diff"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &stubSeriesRouteHandler{}
			router := chi.NewRouter()
			registerSeriesRoutes(router, handler)

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "user"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want %d", rec.Code, http.StatusNoContent)
			}
			if len(handler.called) != 1 || handler.called[0] != tt.called {
				t.Fatalf("called = %v; want [%s]", handler.called, tt.called)
			}
		})
	}
}
