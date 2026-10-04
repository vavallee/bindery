package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// These tests drive the real auth stack (useAPIAuth over the DB backed
// provider) and the real auth handlers to pin the Host header check that
// guards the login free modes. In local-only and disabled mode the mode itself
// admits a caller as the admin, so a browser that reaches Bindery under a
// foreign DNS name must not be served that grant: the name is the only thing
// that tells a page on another site apart from the operator's own tab.

const hostTestAPIKey = "host-check-test-key-0123456789abcdef" // gitleaks:allow

type hostFixture struct {
	handler  http.Handler
	settings *db.SettingsRepo
	provider *dbAuthProvider
	cookie   *http.Cookie
}

func newHostFixture(t *testing.T, mode auth.Mode, trusted string) *hostFixture {
	t.Helper()
	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(conn)
	users := db.NewUserRepo(conn)
	secret := strings.Repeat("s", 32)
	for k, v := range map[string]string{
		api.SettingAuthSessionSecret: secret,
		api.SettingAuthMode:          string(mode),
		api.SettingAuthAPIKey:        hostTestAPIKey,
	} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := users.Create(ctx, "admin", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	provider := &dbAuthProvider{settings: settings, users: users, proxyCIDRs: auth.ParseTrustedProxyCIDRs(trusted)}
	authHandler := api.NewAuthHandler(users, settings, auth.NewLoginLimiter(10, time.Minute))

	r := chi.NewRouter()
	r.Use(trustedProxyMiddleware())
	r.Route("/api/v1", func(r chi.Router) {
		useAPIAuth(r, provider)
		r.Get("/auth/status", authHandler.Status)
		r.Get("/auth/config", authHandler.GetConfig)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Post("/auth/apikey/regenerate", authHandler.RegenerateAPIKey)
		})
	})

	v, err := auth.SignSessionWithEpoch([]byte(secret), admin.ID, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return &hostFixture{
		handler:  r,
		settings: settings,
		provider: provider,
		cookie:   &http.Cookie{Name: auth.SessionCookieName, Value: v},
	}
}

type hostReq struct {
	method  string
	path    string
	host    string
	remote  string
	cookie  *http.Cookie
	apiKey  string
	headers map[string]string
}

func (f *hostFixture) do(q hostReq) *httptest.ResponseRecorder {
	req := httptest.NewRequest(q.method, q.path, nil)
	req.Host = q.host
	req.RemoteAddr = q.remote
	if req.RemoteAddr == "" {
		req.RemoteAddr = "192.168.1.50:51234"
	}
	if q.cookie != nil {
		req.AddCookie(q.cookie)
	}
	if q.apiKey != "" {
		req.Header.Set("X-Api-Key", q.apiKey)
	}
	if q.method != http.MethodGet {
		// A same-origin page can set this header, which is the point: it is
		// no defence against a page the browser treats as same origin.
		req.Header.Set("X-Requested-With", "bindery-ui")
		if q.cookie != nil {
			req.Header.Set("X-CSRF-Token", auth.MakeCSRFToken(f.provider.SessionSecret(), q.cookie.Value))
		}
	}
	for k, v := range q.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func statusOf(t *testing.T, rec *httptest.ResponseRecorder) (authenticated bool, role string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("/auth/status: code %d body %s", rec.Code, rec.Body.String())
	}
	var s struct {
		Authenticated bool   `json:"authenticated"`
		Role          string `json:"role"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s.Authenticated, s.Role
}

func TestHostCheck_ForeignHostRefusedInLoginFreeModes(t *testing.T) {
	for _, mode := range []auth.Mode{auth.ModeLocalOnly, auth.ModeDisabled} {
		for _, host := range []string{
			"attacker.example",
			"attacker.example:8787",
			"ATTACKER.example.",
			"rebind.attacker.example:80",
		} {
			t.Run(string(mode)+"/"+host, func(t *testing.T) {
				f := newHostFixture(t, mode, "")

				rec := f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/config", host: host})
				if rec.Code != http.StatusForbidden {
					t.Fatalf("GET /auth/config: code %d, want 403 (body %s)", rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), hostTestAPIKey) {
					t.Fatal("GET /auth/config leaked the API key to a foreign Host")
				}
				if !strings.Contains(rec.Body.String(), "BINDERY_ALLOWED_HOSTS") {
					t.Errorf("403 body should name BINDERY_ALLOWED_HOSTS, got %s", rec.Body.String())
				}

				rec = f.do(hostReq{method: http.MethodPost, path: "/api/v1/auth/apikey/regenerate", host: host})
				if rec.Code != http.StatusForbidden {
					t.Fatalf("POST /auth/apikey/regenerate: code %d, want 403 (body %s)", rec.Code, rec.Body.String())
				}
				if s, _ := f.settings.Get(context.Background(), api.SettingAuthAPIKey); s == nil || s.Value != hostTestAPIKey {
					t.Fatal("API key was regenerated through a foreign Host")
				}

				// /auth/status must not promise an admin the middleware refuses.
				authed, role := statusOf(t, f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/status", host: host}))
				if authed || role == "admin" {
					t.Errorf("/auth/status: authenticated=%v role=%q under a refused Host", authed, role)
				}
			})
		}
	}
}

func TestHostCheck_ForwardedHostFromTrustedProxyIsChecked(t *testing.T) {
	// trustedProxyMiddleware reads the env var; the provider gets the same list.
	t.Setenv("BINDERY_TRUSTED_PROXY", "10.0.0.2")
	f := newHostFixture(t, auth.ModeLocalOnly, "10.0.0.2")
	rec := f.do(hostReq{
		method: http.MethodGet, path: "/api/v1/auth/config", host: "bindery:8787",
		remote:  "10.0.0.2:40000",
		headers: map[string]string{"X-Forwarded-For": "192.168.1.50", "X-Forwarded-Host": "attacker.example"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code %d, want 403 for a foreign X-Forwarded-Host (body %s)", rec.Code, rec.Body.String())
	}
}

func TestHostCheck_LocalNamesAccepted(t *testing.T) {
	t.Setenv("BINDERY_ALLOWED_HOSTS", " books.example.com , *.home.example.net ")
	t.Setenv("BINDERY_OIDC_REDIRECT_BASE_URL", "https://bindery.example.org/")
	for _, mode := range []auth.Mode{auth.ModeLocalOnly, auth.ModeDisabled} {
		for _, host := range []string{
			"192.168.1.10",
			"192.168.1.10:8787",
			"[fd00::10]:8787",
			"[::1]",
			"localhost:8787",
			"LocalHost",
			"bindery",
			"nas:8787",
			"nas.lan",
			"bindery.local",
			"bindery.home.arpa:443",
			"bindery.internal",
			"nas.localdomain",
			"books.example.com",
			"Books.Example.Com:8443",
			"a.home.example.net",
			"deep.a.home.example.net",
			"bindery.example.org",
		} {
			t.Run(string(mode)+"/"+host, func(t *testing.T) {
				f := newHostFixture(t, mode, "")
				rec := f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/config", host: host})
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), hostTestAPIKey) {
					t.Fatalf("GET /auth/config: code %d, want 200 with the key (body %s)", rec.Code, rec.Body.String())
				}
				authed, role := statusOf(t, f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/status", host: host}))
				if !authed || role != "admin" {
					t.Errorf("/auth/status: authenticated=%v role=%q, want admin", authed, role)
				}
			})
		}
	}
}

func TestHostCheck_WildcardDoesNotMatchApexOrLookalike(t *testing.T) {
	t.Setenv("BINDERY_ALLOWED_HOSTS", "*.home.example.net")
	f := newHostFixture(t, auth.ModeDisabled, "")
	for _, host := range []string{"home.example.net", "evilhome.example.net", "a.home.example.net.attacker.example"} {
		rec := f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/config", host: host})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: code %d, want 403", host, rec.Code)
		}
	}
}

func TestHostCheck_CredentialsUnaffectedByForeignHost(t *testing.T) {
	for _, mode := range []auth.Mode{auth.ModeLocalOnly, auth.ModeDisabled, auth.ModeEnabled} {
		t.Run(string(mode), func(t *testing.T) {
			f := newHostFixture(t, mode, "")
			const host = "bindery.example.com"

			rec := f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/config", host: host, cookie: f.cookie})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), hostTestAPIKey) {
				t.Fatalf("session GET /auth/config: code %d (body %s)", rec.Code, rec.Body.String())
			}
			authed, role := statusOf(t, f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/status", host: host, cookie: f.cookie}))
			if !authed || role != "admin" {
				t.Errorf("session /auth/status: authenticated=%v role=%q", authed, role)
			}

			rec = f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/config", host: host, apiKey: hostTestAPIKey})
			if rec.Code != http.StatusOK {
				t.Fatalf("API key GET /auth/config: code %d (body %s)", rec.Code, rec.Body.String())
			}
			rec = f.do(hostReq{method: http.MethodPost, path: "/api/v1/auth/apikey/regenerate", host: host, apiKey: hostTestAPIKey})
			if rec.Code != http.StatusOK {
				t.Fatalf("API key POST /auth/apikey/regenerate: code %d (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHostCheck_EnabledModeUnchanged(t *testing.T) {
	f := newHostFixture(t, auth.ModeEnabled, "")
	for _, host := range []string{"attacker.example", "192.168.1.10"} {
		rec := f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/config", host: host})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: code %d, want 401 as before", host, rec.Code)
		}
	}
}

func TestHostCheck_RemotePeerInLocalOnlyStill401(t *testing.T) {
	// The Host check sits behind the mode's own network rule: a public peer
	// in local-only mode gets the same 401 as before, whatever its Host.
	f := newHostFixture(t, auth.ModeLocalOnly, "")
	rec := f.do(hostReq{method: http.MethodGet, path: "/api/v1/auth/config", host: "attacker.example", remote: "203.0.113.9:4000"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code %d, want 401", rec.Code)
	}
}
