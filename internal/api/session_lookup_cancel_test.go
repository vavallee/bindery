package api

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
)

// sessionLookupChain wires the real auth middleware in front of the status
// handler and a protected stand-in route, the way the router does.
func sessionLookupChain(p auth.Provider, h *AuthHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/status", h.Status)
	mux.HandleFunc("/api/v1/author", func(w http.ResponseWriter, r *http.Request) {
		if auth.UserIDFromContext(r.Context()) == 0 {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return auth.Middleware(p)(mux)
}

// clearsSessionCookie reports whether rec carries a Set-Cookie that would
// overwrite or expire the session cookie.
func clearsSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return true
		}
	}
	return false
}

// TestSessionLookup_CancelledRequestDoesNotLogOutOthers runs bursts of
// parallel requests on one session against the single connection SQLite
// pool, cancelling one request in each burst at a random point during its
// session lookup. The others must stay signed in, and no response may touch
// the session cookie.
func TestSessionLookup_CancelledRequestDoesNotLogOutOthers(t *testing.T) {
	h, users, settings, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("pw-1234567890")
	if _, err := users.CreateFirstAdmin(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	cookie := loginAndGetCookie(t, h, "admin", "pw-1234567890")
	p := &epochProvider{users: users, secret: []byte(must(settings.Get(ctx, SettingAuthSessionSecret)).Value), h: h}
	chain := sessionLookupChain(p, h)

	const rounds, parallel = 150, 8
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		errs := make(chan string, parallel)
		for i := 0; i < parallel; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				path := "/api/v1/author"
				if i%2 == 0 {
					path = "/api/v1/auth/status"
				}
				reqCtx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cancelled := i == 0
				if cancelled {
					go func() {
						time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond)
						cancel()
					}()
				}
				req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(reqCtx)
				req.AddCookie(cookie)
				rec := httptest.NewRecorder()
				chain.ServeHTTP(rec, req)
				if clearsSessionCookie(rec) {
					errs <- path + ": response touched the session cookie"
				}
				if cancelled {
					if rec.Code == http.StatusUnauthorized {
						errs <- path + ": cancelled request answered 401"
					}
					if path == "/api/v1/auth/status" && rec.Code == http.StatusOK {
						var s statusResponse
						_ = json.Unmarshal(rec.Body.Bytes(), &s)
						if !s.Authenticated {
							errs <- "cancelled status request reported authenticated=false"
						}
					}
					return
				}
				if rec.Code != http.StatusOK {
					errs <- path + ": live request got " + http.StatusText(rec.Code) + " " + rec.Body.String()
					return
				}
				if path == "/api/v1/auth/status" {
					var s statusResponse
					if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || !s.Authenticated {
						errs <- "live status request reported authenticated=false: " + rec.Body.String()
					}
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Fatalf("round %d: %s", round, e)
		}
	}
}

// failingEpochProvider answers the session epoch lookup with err, standing in
// for a database that is briefly unavailable.
type failingEpochProvider struct {
	*epochProvider
	err error
}

func (p *failingEpochProvider) UserSessionEpoch(context.Context, int64) (int64, error) {
	return 0, p.err
}

// TestSessionLookup_FailureIsNotReportedAsSignedOut: the UI decides whether to
// show the login page from GET /auth/status, which the middleware lets through
// without identity. When the session lookup fails, status must answer with a
// server error, not authenticated=false, or a database blip signs the user out
// in the browser.
func TestSessionLookup_FailureIsNotReportedAsSignedOut(t *testing.T) {
	h, users, settings, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("pw-1234567890")
	if _, err := users.CreateFirstAdmin(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	cookie := loginAndGetCookie(t, h, "admin", "pw-1234567890")
	base := &epochProvider{users: users, secret: []byte(must(settings.Get(ctx, SettingAuthSessionSecret)).Value), h: h}

	for _, lookupErr := range []error{errors.New("database is locked"), context.DeadlineExceeded} {
		chain := sessionLookupChain(&failingEpochProvider{epochProvider: base, err: lookupErr}, h)
		for _, path := range []string{"/api/v1/auth/status", "/api/v1/author"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			chain.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK || rec.Code == http.StatusUnauthorized || rec.Code == http.StatusTeapot {
				t.Errorf("%v on %s: status %d body %s; want a server error", lookupErr, path, rec.Code, rec.Body.String())
			}
			if rec.Code < 500 {
				t.Errorf("%v on %s: status %d; want 5xx", lookupErr, path, rec.Code)
			}
			if clearsSessionCookie(rec) {
				t.Errorf("%v on %s: response touched the session cookie", lookupErr, path)
			}
		}
	}
}
