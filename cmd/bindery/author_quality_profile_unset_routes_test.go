package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

type stubQualityProfileUnsetHandler struct{ called int }

func (h *stubQualityProfileUnsetHandler) Count(w http.ResponseWriter, _ *http.Request) {
	h.called++
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubQualityProfileUnsetHandler) Apply(w http.ResponseWriter, r *http.Request) { h.Count(w, r) }

// The count and the bulk apply of #3831 are admin only: the only screen that
// offers them is the admin only Quality tab.
func TestAuthorQualityProfileUnsetRoutesRequireAdmin(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, role := range []string{"user", "requester", ""} {
			h := &stubQualityProfileUnsetHandler{}
			router := chi.NewRouter()
			registerAuthorQualityProfileUnsetRoutes(router, h)
			req := httptest.NewRequest(method, "/authors/without-quality-profile", nil)
			if role != "" {
				req = req.WithContext(auth.WithUserRole(req.Context(), role))
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || h.called != 0 {
				t.Errorf("%s as %q: status %d, called %d; want 403 and no handler", method, role, rec.Code, h.called)
			}
		}
		h := &stubQualityProfileUnsetHandler{}
		router := chi.NewRouter()
		registerAuthorQualityProfileUnsetRoutes(router, h)
		req := httptest.NewRequest(method, "/authors/without-quality-profile", nil)
		req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent || h.called != 1 {
			t.Errorf("%s as admin: status %d, called %d; want 204 and the handler", method, rec.Code, h.called)
		}
	}
}
