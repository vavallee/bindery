package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHealthcheckURL(t *testing.T) {
	for _, tc := range []struct{ port, base, want string }{
		{"8787", "", "http://127.0.0.1:8787/api/v1/health"},
		{"8787", "/bindery", "http://127.0.0.1:8787/bindery/api/v1/health"},
		{"9000", "/media/books", "http://127.0.0.1:9000/media/books/api/v1/health"},
	} {
		if got := healthcheckURL(tc.port, tc.base); got != tc.want {
			t.Errorf("healthcheckURL(%q, %q) = %q, want %q", tc.port, tc.base, got, tc.want)
		}
	}
}

// With a URL base the server only answers under the prefix, so the address the
// healthcheck builds has to reach /api/v1/health through mountUnderURLBase, and
// the unprefixed address the check used before must not (#3352).
func TestHealthcheckURL_ReachesHealthUnderURLBase(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, base := range []string{"", "/bindery"} {
		srv := httptest.NewServer(mountUnderURLBase(r, base))
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		get := func(target string) int {
			t.Helper()
			resp, err := http.Get(target) //nolint:gosec // test server on loopback
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		if got := get(healthcheckURL(u.Port(), base)); got != http.StatusOK {
			t.Errorf("base %q: healthcheck URL answered %d, want 200", base, got)
		}
		if base != "" {
			if got := get(healthcheckURL(u.Port(), "")); got != http.StatusNotFound {
				t.Errorf("base %q: unprefixed URL answered %d, want 404", base, got)
			}
		}
		srv.Close()
	}
}
