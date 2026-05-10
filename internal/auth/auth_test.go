package auth

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("verify should accept the original password")
	}
	if VerifyPassword("wrong password", hash) {
		t.Fatal("verify should reject a wrong password")
	}
	if VerifyPassword("", hash) {
		t.Fatal("verify should reject the empty password")
	}
}

func TestHashPasswordProducesDistinctOutputs(t *testing.T) {
	// Salt is random, so two hashes of the same password should differ.
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("two hashes of the same password produced identical output — salt not random")
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not a phc string",
		"$argon2id$v=19$bogus",
		"$bcrypt$12$notargon$aaaa$bbbb",
	}
	for _, c := range cases {
		if VerifyPassword("whatever", c) {
			t.Errorf("verify accepted malformed hash %q", c)
		}
	}
}

func TestSessionSignAndVerifyRoundtrip(t *testing.T) {
	secret := []byte("test-secret-not-for-production")
	exp := time.Now().Add(time.Hour)
	cookie := SignSession(secret, 42, exp)
	uid, err := VerifySession(secret, cookie)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if uid != 42 {
		t.Errorf("uid = %d; want 42", uid)
	}
}

func TestSessionRejectsTamperedPayload(t *testing.T) {
	secret := []byte("test-secret-not-for-production")
	cookie := SignSession(secret, 1, time.Now().Add(time.Hour))
	// Swap the user id in the payload without re-signing.
	tampered := "v1.9999." + cookie[len("v1.1."):]
	if _, err := VerifySession(secret, tampered); err == nil {
		t.Fatal("verify accepted tampered payload")
	}
}

func TestSessionRejectsWrongSecret(t *testing.T) {
	cookie := SignSession([]byte("one"), 1, time.Now().Add(time.Hour))
	if _, err := VerifySession([]byte("two"), cookie); err == nil {
		t.Fatal("verify accepted cookie signed with a different secret")
	}
}

func TestSessionRejectsExpired(t *testing.T) {
	secret := []byte("s")
	cookie := SignSession(secret, 1, time.Now().Add(-time.Second))
	if _, err := VerifySession(secret, cookie); err == nil {
		t.Fatal("verify accepted an expired cookie")
	}
}

func TestIsLocalIP(t *testing.T) {
	local := []string{"127.0.0.1", "10.1.2.3", "192.168.0.5", "172.16.0.1", "::1", "fe80::1", "fd00::1"}
	remote := []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", ""}
	for _, ip := range local {
		if !IsLocalIP(ip) {
			t.Errorf("IsLocalIP(%q) = false; want true", ip)
		}
	}
	for _, ip := range remote {
		if IsLocalIP(ip) {
			t.Errorf("IsLocalIP(%q) = true; want false", ip)
		}
	}
}

func TestIsLocalRequestParsesHostPort(t *testing.T) {
	req := &http.Request{RemoteAddr: "192.168.1.10:54321"}
	if !IsLocalRequest(req) {
		t.Fatal("RemoteAddr with port should still match local CIDR")
	}
	req = &http.Request{RemoteAddr: "8.8.8.8:443"}
	if IsLocalRequest(req) {
		t.Fatal("8.8.8.8 should not be local")
	}
}

func TestLoginLimiterEnforcesMax(t *testing.T) {
	lim := NewLoginLimiter(3, time.Minute)
	ip := "1.2.3.4"
	for i := range 3 {
		if !lim.Allow(ip) {
			t.Fatalf("attempt %d should be allowed", i)
		}
		lim.Record(ip)
	}
	if lim.Allow(ip) {
		t.Fatal("4th attempt should be blocked")
	}
}

func TestLoginLimiterResetOnSuccess(t *testing.T) {
	lim := NewLoginLimiter(3, time.Minute)
	ip := "1.2.3.4"
	lim.Record(ip)
	lim.Record(ip)
	lim.Reset(ip)
	if !lim.Allow(ip) {
		t.Fatal("Reset should clear the counter")
	}
}

// TestLoginLimiterConfigurableThreshold verifies that a limiter built with a
// non-default max blocks exactly at that threshold, not at the hardcoded default of 5.
func TestLoginLimiterConfigurableThreshold(t *testing.T) {
	const customMax = 3
	lim := NewLoginLimiter(customMax, time.Minute)
	ip := "10.0.0.1"

	for i := range customMax {
		if !lim.Allow(ip) {
			t.Fatalf("attempt %d should be allowed (max=%d)", i+1, customMax)
		}
		lim.Record(ip)
	}
	// Next attempt must be blocked at customMax, not at 5.
	if lim.Allow(ip) {
		t.Fatalf("attempt %d should be blocked (max=%d)", customMax+1, customMax)
	}
}

func TestLoginLimiterExpiresOldEvents(t *testing.T) {
	lim := NewLoginLimiter(2, 10*time.Millisecond)
	ip := "1.2.3.4"
	lim.Record(ip)
	lim.Record(ip)
	if lim.Allow(ip) {
		t.Fatal("should be blocked immediately after filling the bucket")
	}
	time.Sleep(20 * time.Millisecond)
	if !lim.Allow(ip) {
		t.Fatal("bucket should drain after window elapses")
	}
}

// --- middleware integration --------------------------------------------------

type fakeProvider struct {
	mode           Mode
	apiKey         string
	secret         []byte
	setup          bool
	proxyHeader    string
	proxyProvision bool
	proxyCIDRs     []*net.IPNet
	provisioner    UserProvisioner
}

func (f *fakeProvider) Mode() Mode                                 { return f.mode }
func (f *fakeProvider) APIKey() string                             { return f.apiKey }
func (f *fakeProvider) SessionSecret() []byte                      { return f.secret }
func (f *fakeProvider) SetupRequired() bool                        { return f.setup }
func (f *fakeProvider) ProxyAuthHeader() string                    { return f.proxyHeader }
func (f *fakeProvider) ProxyAutoProvision() bool                   { return f.proxyProvision }
func (f *fakeProvider) TrustedProxyCIDRs() []*net.IPNet            { return f.proxyCIDRs }
func (f *fakeProvider) UserRole(_ context.Context, _ int64) string { return "admin" }
func (f *fakeProvider) UserProvisioner() UserProvisioner           { return f.provisioner }

// staticProvisioner always returns the same user ID for any username.
type staticProvisioner struct{ uid int64 }

func (s *staticProvisioner) ResolveOrProvisionUser(_ context.Context, _ string, _ bool) (int64, error) {
	return s.uid, nil
}

func mustParseCIDR(s string) *net.IPNet {
	_, cidr, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return cidr
}

func TestMiddlewareAllowsHealthWithoutAuth(t *testing.T) {
	p := &fakeProvider{mode: ModeEnabled}
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/health", nil)
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("health endpoint should be allowed unauthenticated")
	}
}

func TestMiddlewareAllowsLocalWhenLocalOnly(t *testing.T) {
	p := &fakeProvider{mode: ModeLocalOnly}
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.RemoteAddr = "192.168.1.5:12345"
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("local-only mode must pass LAN requests through")
	}
}

func TestMiddlewareBlocksRemoteWhenLocalOnly(t *testing.T) {
	p := &fakeProvider{mode: ModeLocalOnly, apiKey: "valid"}
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("remote request without credentials must be blocked")
	}
	if w.status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", w.status)
	}
}

func TestMiddlewareAcceptsValidAPIKey(t *testing.T) {
	p := &fakeProvider{mode: ModeEnabled, apiKey: "correct-key"}
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.Header.Set("X-Api-Key", "correct-key")
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("valid API key must pass")
	}
}

func TestMiddlewareAcceptsAPIKeyInQuery(t *testing.T) {
	p := &fakeProvider{mode: ModeEnabled, apiKey: "correct-key"}
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author?apikey=correct-key", nil)
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("API key via ?apikey= query param must pass")
	}
}

func TestMiddlewareAcceptsValidSessionCookie(t *testing.T) {
	secret := []byte("hmac-secret")
	p := &fakeProvider{mode: ModeEnabled, secret: secret}
	mw := Middleware(p)
	var gotUID int64
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotUID = UserIDFromContext(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: SignSession(secret, 7, time.Now().Add(time.Hour))})
	h.ServeHTTP(nopWriter{}, req)
	if gotUID != 7 {
		t.Errorf("uid = %d; want 7", gotUID)
	}
}

// --- test helpers ------------------------------------------------------------

type nopWriter struct{}

func (nopWriter) Header() http.Header         { return http.Header{} }
func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) WriteHeader(int)             {}

type captureWriter struct {
	status int
	header http.Header
}

func (c *captureWriter) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}
func (c *captureWriter) Write(b []byte) (int, error) { return len(b), nil }
func (c *captureWriter) WriteHeader(s int)           { c.status = s }

func TestParseMode(t *testing.T) {
	if m := ParseMode("enabled"); m != ModeEnabled {
		t.Errorf("want ModeEnabled, got %q", m)
	}
	if m := ParseMode("local-only"); m != ModeLocalOnly {
		t.Errorf("want ModeLocalOnly, got %q", m)
	}
	if m := ParseMode("disabled"); m != ModeDisabled {
		t.Errorf("want ModeDisabled, got %q", m)
	}
	// unknown → defaults to enabled
	if m := ParseMode("garbage"); m != ModeEnabled {
		t.Errorf("unknown mode should default to ModeEnabled, got %q", m)
	}
	if m := ParseMode(""); m != ModeEnabled {
		t.Errorf("empty mode should default to ModeEnabled, got %q", m)
	}
	if m := ParseMode("proxy"); m != ModeProxy {
		t.Errorf("want ModeProxy, got %q", m)
	}
}

// --- proxy mode tests --------------------------------------------------------

func proxyProvider(trustedCIDR string, autoProvision bool, uid int64) *fakeProvider {
	return &fakeProvider{
		mode:           ModeProxy,
		proxyHeader:    "X-Forwarded-User",
		proxyProvision: autoProvision,
		proxyCIDRs:     []*net.IPNet{mustParseCIDR(trustedCIDR)},
		provisioner:    &staticProvisioner{uid: uid},
	}
}

func TestProxyAuthTrustedIPWithHeader(t *testing.T) {
	p := proxyProvider("10.0.0.0/8", true, 42)
	mw := Middleware(p)
	var gotUID int64
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotUID = UserIDFromContext(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.RemoteAddr = "10.0.0.5:12345"
	req.Header.Set("X-Forwarded-User", "alice")
	h.ServeHTTP(nopWriter{}, req)
	if gotUID != 42 {
		t.Errorf("uid = %d; want 42", gotUID)
	}
}

func TestProxyAuthUntrustedIPWithHeader(t *testing.T) {
	p := proxyProvider("10.0.0.0/8", true, 42)
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	req.Header.Set("X-Forwarded-User", "alice")
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("untrusted IP with identity header must be rejected")
	}
	if w.status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", w.status)
	}
}

func TestProxyAuthTrustedIPNoHeader(t *testing.T) {
	p := proxyProvider("10.0.0.0/8", true, 42)
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.RemoteAddr = "10.0.0.5:12345"
	// No X-Forwarded-User header
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("trusted IP with no identity header must be rejected")
	}
	if w.status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", w.status)
	}
}

// --- CSRF tests --------------------------------------------------------

func TestMakeCSRFToken_DifferentSessionsDiffer(t *testing.T) {
	secret := []byte("test-secret")
	tok1 := MakeCSRFToken(secret, "session-value-a")
	tok2 := MakeCSRFToken(secret, "session-value-b")
	if tok1 == tok2 {
		t.Fatal("tokens for different sessions must differ")
	}
}

func TestMakeCSRFToken_SameInputsSameOutput(t *testing.T) {
	secret := []byte("test-secret")
	tok1 := MakeCSRFToken(secret, "session-value")
	tok2 := MakeCSRFToken(secret, "session-value")
	if tok1 != tok2 {
		t.Fatal("same inputs must produce the same token")
	}
}

func TestRequireCSRFToken_AllowsSafeMethod(t *testing.T) {
	secret := []byte("s")
	mw := RequireCSRFToken(func() []byte { return secret })
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("GET must pass without CSRF token")
	}
}

func TestRequireCSRFToken_AllowsUnauthPath(t *testing.T) {
	secret := []byte("s")
	mw := RequireCSRFToken(func() []byte { return secret })
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	// AllowUnauthPath routes must always pass through, even with a stale session cookie.
	req, _ := http.NewRequest("POST", "/api/v1/auth/login", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "stale-cookie-value"})
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("POST to AllowUnauthPath must pass through regardless of session cookie")
	}
}

func TestRequireCSRFToken_BlocksMutationWithSessionButNoToken(t *testing.T) {
	secret := []byte("s")
	sessionVal := SignSession(secret, 1, time.Now().Add(time.Hour))
	mw := RequireCSRFToken(func() []byte { return secret })
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	// Session cookie present but no CSRF token — cross-origin attack scenario.
	req, _ := http.NewRequest("POST", "/api/v1/author", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("POST with session cookie but no CSRF token must be rejected")
	}
	if w.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.status)
	}
}

func TestRequireCSRFToken_AllowsMutationWithAPIKey(t *testing.T) {
	secret := []byte("s")
	mw := RequireCSRFToken(func() []byte { return secret })
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("POST", "/api/v1/author", nil)
	req.Header.Set("X-Api-Key", "apikey-value")
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("POST with API key must bypass CSRF check")
	}
}

func TestRequireCSRFToken_AllowsMutationWithValidToken(t *testing.T) {
	secret := []byte("test-secret")
	sessionVal := SignSession(secret, 1, time.Now().Add(time.Hour))
	token := MakeCSRFToken(secret, sessionVal)

	mw := RequireCSRFToken(func() []byte { return secret })
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("DELETE", "/api/v1/author/1", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	req.Header.Set("X-CSRF-Token", token)
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("DELETE with valid CSRF token must pass")
	}
}

func TestRequireCSRFToken_BlocksMutationWithWrongToken(t *testing.T) {
	secret := []byte("test-secret")
	sessionVal := SignSession(secret, 1, time.Now().Add(time.Hour))

	mw := RequireCSRFToken(func() []byte { return secret })
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("PUT", "/api/v1/author/1", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	req.Header.Set("X-CSRF-Token", "bad-token")
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("PUT with wrong CSRF token must be rejected")
	}
	if w.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.status)
	}
}

// --- RequireXRequestedWith tests -----------------------------------------------

func TestRequireXRequestedWith_AllowsSafeMethod(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/queue", nil)
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("GET must pass through without X-Requested-With")
	}
}

func TestRequireXRequestedWith_BlocksMutationWithoutHeader(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("POST", "/api/v1/queue/grab", nil)
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("POST without X-Requested-With and without API key must be rejected")
	}
	if w.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.status)
	}
}

func TestRequireXRequestedWith_AllowsMutationWithAPIKeyHeader(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("POST", "/api/v1/queue/grab", nil)
	req.Header.Set("X-Api-Key", "some-api-key")
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("POST with X-Api-Key must bypass RequireXRequestedWith")
	}
}

func TestRequireXRequestedWith_AllowsMutationWithAPIKeyQuery(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("DELETE", "/api/v1/queue/1?apikey=some-api-key", nil)
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("DELETE with ?apikey= must bypass RequireXRequestedWith")
	}
}

func TestRequireXRequestedWith_AllowsMutationWithCorrectHeader(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("POST", "/api/v1/queue/grab", nil)
	req.Header.Set("X-Requested-With", "bindery-ui")
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("POST with correct X-Requested-With must pass")
	}
}

// --- Full CSRF stack integration tests (Issue #424) ----------------------------
//
// These tests mirror the middleware chain applied to /api/v1 in main.go:
// auth.Middleware → RequireXRequestedWith → RequireCSRFToken → handler.
// They verify that external Arr clients (Harpoon, etc.) can reach
// /api/queue using an API key while browser sessions still require both
// CSRF guards.

func buildCSRFStack(p Provider, secret []byte, inner http.Handler) http.Handler {
	return Middleware(p)(
		RequireXRequestedWith(
			RequireCSRFToken(func() []byte { return secret })(inner),
		),
	)
}

func TestCSRFStack_APIKeyRequestPassesAllGuards(t *testing.T) {
	secret := []byte("stack-secret")
	p := &fakeProvider{mode: ModeEnabled, apiKey: "harpoon-key", secret: secret}
	called := false
	stack := buildCSRFStack(p, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	// Simulate Harpoon: API key only, no browser headers.
	req, _ := http.NewRequest("GET", "/api/v1/queue", nil)
	req.Header.Set("X-Api-Key", "harpoon-key")
	req.RemoteAddr = "10.0.0.5:50000"
	rw := &captureWriter{}
	stack.ServeHTTP(rw, req)
	if !called {
		t.Fatal("API-key GET to /api/queue must reach the handler")
	}
	if rw.status != http.StatusOK {
		t.Errorf("status = %d; want 200", rw.status)
	}
}

func TestCSRFStack_APIKeyGrabPassesAllGuards(t *testing.T) {
	secret := []byte("stack-secret")
	p := &fakeProvider{mode: ModeEnabled, apiKey: "harpoon-key", secret: secret}
	called := false
	stack := buildCSRFStack(p, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	// Simulate Harpoon: POST /api/queue/grab with API key, no CSRF headers.
	req, _ := http.NewRequest("POST", "/api/v1/queue/grab", nil)
	req.Header.Set("X-Api-Key", "harpoon-key")
	req.RemoteAddr = "10.0.0.5:50000"
	rw := &captureWriter{}
	stack.ServeHTTP(rw, req)
	if !called {
		t.Fatal("API-key POST to /api/queue/grab must reach the handler")
	}
}

// TestRequireXRequestedWith_AllowsLoginEndpoint verifies that POST /api/v1/auth/login
// passes through RequireXRequestedWith without the custom header. Before a session
// exists there is no cookie to CSRF-protect, so blocking the login endpoint from plain
// curl/scripts is pure friction with no security benefit.
// Regression test for Bug 4: login unreachable from non-browser clients.
func TestRequireXRequestedWith_AllowsLoginEndpoint(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	// No X-Requested-With, no API key — plain curl.
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("POST /api/v1/auth/login must not be blocked by RequireXRequestedWith — no session to CSRF-protect")
	}
}

// TestRequireXRequestedWith_AllowsSetupEndpoint mirrors the login case for the
// first-run /auth/setup endpoint. Setup creates the very first user; there is
// no authenticated session and therefore no CSRF risk.
func TestRequireXRequestedWith_AllowsSetupEndpoint(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/setup", nil)
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("POST /api/v1/auth/setup must not be blocked by RequireXRequestedWith")
	}
}

func TestCSRFStack_BrowserSessionWithoutCSRFTokenIsRejected(t *testing.T) {
	secret := []byte("stack-secret")
	p := &fakeProvider{mode: ModeEnabled, apiKey: "harpoon-key", secret: secret}
	called := false
	stack := buildCSRFStack(p, secret, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))

	// Browser session cookie present but no CSRF token — CSRF attack scenario.
	sessionVal := SignSession(secret, 1, time.Now().Add(time.Hour))
	req, _ := http.NewRequest("POST", "/api/v1/queue/grab", nil)
	req.Header.Set("X-Requested-With", "bindery-ui")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	// Deliberately omit X-CSRF-Token.
	req.RemoteAddr = "8.8.8.8:12345"
	rw := &captureWriter{}
	stack.ServeHTTP(rw, req)
	if called {
		t.Fatal("browser session POST without CSRF token must be rejected")
	}
	if rw.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", rw.status)
	}
}
