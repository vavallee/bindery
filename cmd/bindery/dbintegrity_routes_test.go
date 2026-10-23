package main

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

type stubDBIntegrityHandler struct{ called int }

func (h *stubDBIntegrityHandler) Check(w http.ResponseWriter, _ *http.Request) {
	h.called++
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubDBIntegrityHandler) Preview(w http.ResponseWriter, r *http.Request) { h.Check(w, r) }
func (h *stubDBIntegrityHandler) Cleanup(w http.ResponseWriter, r *http.Request) { h.Check(w, r) }

// The route table is exactly three POSTs. A GET here would let a link prefetcher
// or crawler start a full database scan.
func TestDBIntegrityRoutesEnumerated(t *testing.T) {
	router := chi.NewRouter()
	registerDBIntegrityRoutes(router, &stubDBIntegrityHandler{})
	var mounted []string
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted = append(mounted, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(mounted)
	if got := strings.Join(mounted, "\n"); got != "POST /system/db-integrity\nPOST /system/db-integrity/cleanup\nPOST /system/db-integrity/cleanup/preview" {
		t.Fatalf("mounted routes:\n%s", got)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/system/db-integrity", nil))
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("GET status = %d, want 405/404", rec.Code)
	}
}

func TestDBIntegrityRouteRequiresAdmin(t *testing.T) {
	for _, role := range []string{"user", "requester", ""} {
		h := &stubDBIntegrityHandler{}
		router := chi.NewRouter()
		registerDBIntegrityRoutes(router, h)
		req := httptest.NewRequest(http.MethodPost, "/system/db-integrity", nil)
		if role != "" {
			req = req.WithContext(auth.WithUserRole(req.Context(), role))
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || h.called != 0 {
			t.Errorf("role %q: status %d, called %d; want 403 and no handler", role, rec.Code, h.called)
		}
	}
	h := &stubDBIntegrityHandler{}
	router := chi.NewRouter()
	registerDBIntegrityRoutes(router, h)
	req := httptest.NewRequest(http.MethodPost, "/system/db-integrity", nil)
	req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || h.called != 1 {
		t.Errorf("admin: status %d, called %d", rec.Code, h.called)
	}
}

// Through the real auth stack: no session is 401, a plain user and a requester
// are 403, the admin gets through.
func TestDBIntegrityRouteThroughAuthStack(t *testing.T) {
	f := newRequesterFixture(t, "")
	const path = "/api/v1/system/db-integrity"

	if rec := f.do(http.MethodPost, path, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: %d, want 401", rec.Code)
	}
	if rec := f.do(http.MethodPost, path, f.userCookie); rec.Code != http.StatusForbidden {
		t.Errorf("role user: %d, want 403", rec.Code)
	}
	if rec := f.do(http.MethodPost, path, f.cookie); rec.Code != http.StatusForbidden {
		t.Errorf("requester: %d, want 403", rec.Code)
	}
	v, err := auth.SignSessionWithEpoch([]byte(strings.Repeat("s", 32)), 1, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Cookie{Name: auth.SessionCookieName, Value: v}
	if rec := f.do(http.MethodPost, path, admin); rec.Code != http.StatusNoContent {
		t.Errorf("admin: %d, want 204", rec.Code)
	}
}
