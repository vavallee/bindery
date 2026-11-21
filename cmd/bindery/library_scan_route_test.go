package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
)

type scanTriggerStub struct{ started *int }

func (s scanTriggerStub) Scan(w http.ResponseWriter, _ *http.Request) {
	*s.started++
	w.WriteHeader(http.StatusAccepted)
}

func (s scanTriggerStub) ScanStatus(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// TestLibraryScanTriggerRouteRequiresAdmin: POST /library/scan walks the whole
// library with no owner filter, so under multi user enforcement a non admin (or
// a request with no role) is refused before the handler starts a scan, while an
// admin still gets 202. Tenancy off and API key requests carry the admin role,
// so they are unchanged.
func TestLibraryScanTriggerRouteRequiresAdmin(t *testing.T) {
	cases := []struct {
		role        string
		want        int
		wantStarted int
	}{
		{"user", http.StatusForbidden, 0},
		{"", http.StatusForbidden, 0},
		{"admin", http.StatusAccepted, 1},
	}
	for _, tc := range cases {
		t.Run("role="+tc.role, func(t *testing.T) {
			started := 0
			router := chi.NewRouter()
			registerLibraryScanStatusRoute(router, scanTriggerStub{started: &started})

			req := httptest.NewRequest(http.MethodPost, "/library/scan", nil)
			if tc.role != "" {
				req = req.WithContext(auth.WithUserRole(req.Context(), tc.role))
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want || started != tc.wantStarted {
				t.Fatalf("role=%q status = %d started = %d; want %d and %d", tc.role, rec.Code, started, tc.want, tc.wantStarted)
			}
		})
	}
}
