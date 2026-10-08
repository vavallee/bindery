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

// These tests drive proxy auth through the real request stack:
// trustedProxyMiddleware (which rewrites RemoteAddr from X-Forwarded-For for a
// trusted peer), the URL base mount, useAPIAuth over the DB backed provider,
// and the real /auth/status handler. #3096: behind Cloudflare Tunnel, or any
// proxy that sets X-Forwarded-For, the identity header was rejected because the
// trust check ran on the rewritten client address instead of the proxy.

const (
	proxyFwdTrusted = "172.17.0.0/16"
	proxyFwdHeader  = "Cf-Access-Authenticated-User-Email"
	proxyFwdUser    = "user@example.com"
)

func newProxyForwardedHandler(t *testing.T, urlBase string) http.Handler {
	t.Helper()
	// trustedProxyMiddleware reads the env var; the provider gets the same
	// list, as main wires both from BINDERY_TRUSTED_PROXY.
	t.Setenv("BINDERY_TRUSTED_PROXY", proxyFwdTrusted)

	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(conn)
	users := db.NewUserRepo(conn)
	for k, v := range map[string]string{
		api.SettingAuthSessionSecret: strings.Repeat("s", 32),
		api.SettingAuthMode:          string(auth.ModeProxy),
		api.SettingAuthAPIKey:        "proxy-forwarded-test-key-0123456789", // gitleaks:allow
	} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	// Auto provisioning off and the user created up front, as in the report.
	if _, err := users.Create(ctx, proxyFwdUser, "x"); err != nil {
		t.Fatal(err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	provider := &dbAuthProvider{
		settings:       settings,
		users:          users,
		proxyHeader:    proxyFwdHeader,
		proxyProvision: false,
		proxyCIDRs:     auth.ParseTrustedProxyCIDRs(proxyFwdTrusted),
	}
	authHandler := api.NewAuthHandler(users, settings, auth.NewLoginLimiter(10, time.Minute))

	r := chi.NewRouter()
	r.Use(trustedProxyMiddleware())
	r.Route("/api/v1", func(r chi.Router) {
		useAPIAuth(r, provider)
		r.Get("/auth/status", authHandler.Status)
		r.Get("/whoami", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]int64{"uid": auth.UserIDFromContext(r.Context())})
		})
	})
	return mountUnderURLBase(r, urlBase)
}

type proxyFwdCase struct {
	name     string
	peer     string
	headers  map[string]string
	wantAuth bool
}

func TestProxyAuthBehindForwardingProxy(t *testing.T) {
	cases := []proxyFwdCase{
		{
			// The #3096 shape: cloudflared on the Docker bridge forwards a
			// visitor on the internet.
			name:     "trusted peer forwarding an untrusted client",
			peer:     "172.17.0.1:40000",
			headers:  map[string]string{proxyFwdHeader: proxyFwdUser, "X-Forwarded-For": "203.0.113.9"},
			wantAuth: true,
		},
		{
			name: "trusted peer forwarding a multi hop chain",
			peer: "172.17.0.1:40000",
			headers: map[string]string{
				proxyFwdHeader:    proxyFwdUser,
				"X-Forwarded-For": "198.51.100.20, 203.0.113.9",
			},
			wantAuth: true,
		},
		{
			name:     "trusted peer without X-Forwarded-For (unchanged)",
			peer:     "172.17.0.1:40000",
			headers:  map[string]string{proxyFwdHeader: proxyFwdUser},
			wantAuth: true,
		},
		{
			name:     "untrusted peer without X-Forwarded-For (unchanged)",
			peer:     "198.51.100.7:40000",
			headers:  map[string]string{proxyFwdHeader: proxyFwdUser},
			wantAuth: false,
		},
		{
			// Spoofing: a host on the internet sends the identity header and
			// claims to be the proxy. Its forwarded headers are stripped and
			// the decision is made on its own address.
			name:     "untrusted peer claiming a trusted IP in X-Forwarded-For",
			peer:     "198.51.100.7:40000",
			headers:  map[string]string{proxyFwdHeader: proxyFwdUser, "X-Forwarded-For": "172.17.0.1"},
			wantAuth: false,
		},
		{
			name: "untrusted peer claiming a trusted IP in every forwarded header",
			peer: "198.51.100.7:40000",
			headers: map[string]string{
				proxyFwdHeader:    proxyFwdUser,
				"X-Forwarded-For": "172.17.0.1",
				"X-Real-IP":       "172.17.0.1",
				"True-Client-IP":  "172.17.0.1",
			},
			wantAuth: false,
		},
		{
			name:     "trusted peer forwarding without the identity header",
			peer:     "172.17.0.1:40000",
			headers:  map[string]string{"X-Forwarded-For": "203.0.113.9"},
			wantAuth: false,
		},
	}

	for _, base := range []string{"", "/request"} {
		for _, tc := range cases {
			t.Run("base="+base+"/"+tc.name, func(t *testing.T) {
				h := newProxyForwardedHandler(t, base)

				do := func(path string) *httptest.ResponseRecorder {
					req := httptest.NewRequest(http.MethodGet, base+path, nil)
					req.RemoteAddr = tc.peer
					for k, v := range tc.headers {
						req.Header.Set(k, v)
					}
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					return rec
				}

				rec := do("/api/v1/auth/status")
				if rec.Code != http.StatusOK {
					t.Fatalf("/auth/status: code %d body %s", rec.Code, rec.Body.String())
				}
				var st struct {
					Authenticated bool   `json:"authenticated"`
					Username      string `json:"username"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
					t.Fatal(err)
				}
				if st.Authenticated != tc.wantAuth {
					t.Fatalf("/auth/status authenticated = %v, want %v (body %s)", st.Authenticated, tc.wantAuth, rec.Body.String())
				}
				if tc.wantAuth && st.Username != proxyFwdUser {
					t.Fatalf("/auth/status username = %q, want %q", st.Username, proxyFwdUser)
				}

				rec = do("/api/v1/whoami")
				if !tc.wantAuth {
					if rec.Code != http.StatusUnauthorized {
						t.Fatalf("protected route: code %d, want 401 (body %s)", rec.Code, rec.Body.String())
					}
					return
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("protected route: code %d, want 200 (body %s)", rec.Code, rec.Body.String())
				}
				var who struct {
					UID int64 `json:"uid"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &who); err != nil {
					t.Fatal(err)
				}
				if who.UID == 0 {
					t.Fatal("protected route reached without the proxy identity on the context")
				}
			})
		}
	}
}
