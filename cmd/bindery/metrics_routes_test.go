package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/metrics"
)

// metricsTestRouter mirrors the shape of the production router that matters
// for metric labels: the metrics middleware at the root (before auth), a
// fixed route, an /api/v1 subrouter with a templated route, and the SPA GET
// catch-all.
func metricsTestRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(metrics.HTTPMiddleware(routeTemplate))
	r.Get("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/book/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})
	r.Get("/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return r
}

// httpSeriesCount returns the number of distinct label sets currently held by
// the request counter and the request duration histogram.
func httpSeriesCount(t *testing.T) int {
	t.Helper()
	mfs, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	n := 0
	for _, mf := range mfs {
		switch mf.GetName() {
		case "bindery_http_requests_total", "bindery_http_request_duration_seconds":
			n += len(mf.GetMetric())
		}
	}
	return n
}

// TestMetricsMiddleware_UnauthenticatedRequestsCannotGrowSeries is the
// regression test for the label cardinality leak: the middleware runs before
// auth, so every label value derived from the request must come from a
// bounded set. Invented methods and unmatched paths must collapse onto a
// constant number of series no matter how many distinct values arrive.
func TestMetricsMiddleware_UnauthenticatedRequestsCannotGrowSeries(t *testing.T) {
	h := metricsTestRouter()

	send := func(method, path string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Method = method
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	// Warm every shape once so the first observation of each bounded label
	// set is not counted against the flood below.
	send("FOOWARM", "/warm")
	send(http.MethodPost, "/warm")
	send(http.MethodGet, "/api/v1/warm/nope")
	send("FOOWARM", "/api/v1/warm")
	send(http.MethodGet, "/warm")
	before := httpSeriesCount(t)

	const n = 200
	for i := 0; i < n; i++ {
		send(fmt.Sprintf("FOO%d", i), fmt.Sprintf("/x%d", i))
		send(http.MethodPost, fmt.Sprintf("/y%d", i))
		send(http.MethodGet, fmt.Sprintf("/api/v1/z%d/nope", i))
		send(fmt.Sprintf("BAR%d", i), fmt.Sprintf("/api/v1/w%d", i))
		send(http.MethodGet, fmt.Sprintf("/spa/%d", i))
	}

	if grew := httpSeriesCount(t) - before; grew != 0 {
		t.Fatalf("%d requests with invented methods and unmatched paths created %d new series; want 0", 5*n, grew)
	}
}

// TestRouteTemplate_NeverReturnsRawPath pins the route label for the cases
// that matter: chi's template when a route matched, a fixed value otherwise.
func TestRouteTemplate_NeverReturnsRawPath(t *testing.T) {
	var got string
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			got = routeTemplate(req)
		})
	})
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/book/{id}", func(w http.ResponseWriter, _ *http.Request) {})
	})
	r.Get("/*", func(w http.ResponseWriter, _ *http.Request) {})

	cases := []struct {
		method, path, want string
	}{
		{http.MethodGet, "/api/v1/book/42", "/api/v1/book/{id}"},
		{http.MethodGet, "/api/v1/nope/123", "/api/v1/*"},
		{http.MethodGet, "/some/spa/page", "/*"},
		{http.MethodPost, "/some/spa/page", unmatchedRoute},
		{"PROPFIND", "/secret-path-abc", unmatchedRoute},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Method = tc.method
		r.ServeHTTP(httptest.NewRecorder(), req)
		if got != tc.want {
			t.Errorf("%s %s: route=%q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
