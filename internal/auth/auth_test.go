package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
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

// testSecret32 is a 32-byte secret suitable for session signing in tests.
var testSecret32 = []byte("test-secret-32bytes-for-testing!")

func TestSessionSignAndVerifyRoundtrip(t *testing.T) {
	secret := testSecret32
	exp := time.Now().Add(time.Hour)
	cookie, err := SignSession(secret, 42, exp)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	uid, err := VerifySession(secret, cookie)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if uid != 42 {
		t.Errorf("uid = %d; want 42", uid)
	}
}

func TestSessionRejectsTamperedPayload(t *testing.T) {
	secret := testSecret32
	cookie, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Swap the user id in the payload without re-signing.
	tampered := "v1.9999." + cookie[len("v1.1."):]
	if _, err := VerifySession(secret, tampered); err == nil {
		t.Fatal("verify accepted tampered payload")
	}
}

func TestSessionRejectsWrongSecret(t *testing.T) {
	secretA := []byte("secret-A-for-signing-32bytes!!!!") // 32 bytes
	secretB := []byte("secret-B-for-verify-32bytes!!!!")  // 32 bytes
	cookie, err := SignSession(secretA, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := VerifySession(secretB, cookie); err == nil {
		t.Fatal("verify accepted cookie signed with a different secret")
	}
}

func TestSessionRejectsExpired(t *testing.T) {
	secret := testSecret32
	cookie, err := SignSession(secret, 1, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := VerifySession(secret, cookie); err == nil {
		t.Fatal("verify accepted an expired cookie")
	}
}

func TestSessionRejectsShortSecret(t *testing.T) {
	if _, err := SignSession([]byte("tooshort"), 1, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("SignSession must reject a secret shorter than 32 bytes")
	}
	if _, err := VerifySession([]byte("tooshort"), "v1.1.9999999999.AAAA"); err == nil {
		t.Fatal("VerifySession must reject a secret shorter than 32 bytes")
	}
	if _, err := VerifySession(nil, "v1.1.9999999999.AAAA"); err == nil {
		t.Fatal("VerifySession must reject a nil secret")
	}
}

func TestSessionSentinelErrors(t *testing.T) {
	secret := testSecret32

	// Expired cookie must wrap ErrSessionExpired.
	cookie, err := SignSession(secret, 1, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = VerifySession(secret, cookie)
	if !errors.Is(err, ErrSessionExpired) {
		t.Errorf("expired cookie: want errors.Is(err, ErrSessionExpired); got %v", err)
	}

	// Tampered cookie must wrap ErrSessionInvalid.
	good, _ := SignSession(secret, 1, time.Now().Add(time.Hour))
	tampered := "v1.9999." + good[len("v1.1."):]
	_, err = VerifySession(secret, tampered)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("tampered cookie: want errors.Is(err, ErrSessionInvalid); got %v", err)
	}

	// Short secret must wrap ErrSessionInvalid.
	_, err = VerifySession([]byte("short"), good)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("short secret: want errors.Is(err, ErrSessionInvalid); got %v", err)
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
	secret := testSecret32
	p := &fakeProvider{mode: ModeEnabled, secret: secret}
	mw := Middleware(p)
	var gotUID int64
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotUID = UserIDFromContext(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	cookie, err := SignSession(secret, 7, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
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
	secret := testSecret32
	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
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
	// A request whose key Middleware already verified carries the
	// AuthedViaAPIKey context flag. The CSRF guard exempts it on that flag.
	req = req.WithContext(WithAPIKeyAuth(req.Context()))
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("POST with verified API key must bypass CSRF check")
	}
}

// TestRequireCSRFToken_BogusAPIKeyDoesNotBypassCSRF is the core regression test
// for #708 finding 3. A request with a *bogus* ?apikey= (or X-Api-Key) was
// previously exempted from CSRF purely because the parameter was present, even
// though the bogus key fails verification and the request authenticates via the
// session cookie. After the fix the exemption keys off the verified-auth
// context flag, which a bogus key never sets, so the CSRF token is enforced.
func TestRequireCSRFToken_BogusAPIKeyDoesNotBypassCSRF(t *testing.T) {
	secret := testSecret32
	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	mw := RequireCSRFToken(func() []byte { return secret })
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))

	// Bogus apikey in the query string, real session cookie, NO CSRF token.
	// Crucially: no AuthedViaAPIKey flag in context (Middleware never set it
	// because the key would fail subtle.ConstantTimeCompare).
	req, _ := http.NewRequest("POST", "/api/v1/author?apikey=bogus-not-the-real-key", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("POST with bogus ?apikey= and a session cookie must NOT bypass CSRF (#708)")
	}
	if w.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.status)
	}
}

// TestRequireCSRFToken_BogusAPIKeyHeaderDoesNotBypassCSRF mirrors the above for
// a bogus X-Api-Key *header* — the header is no exemption either; only the
// verified-auth context flag is.
func TestRequireCSRFToken_BogusAPIKeyHeaderDoesNotBypassCSRF(t *testing.T) {
	secret := testSecret32
	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	mw := RequireCSRFToken(func() []byte { return secret })
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))

	req, _ := http.NewRequest("DELETE", "/api/v1/author/1", nil)
	req.Header.Set("X-Api-Key", "bogus-not-the-real-key")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("DELETE with bogus X-Api-Key and a session cookie must NOT bypass CSRF (#708)")
	}
	if w.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.status)
	}
}

func TestRequireCSRFToken_AllowsMutationWithValidToken(t *testing.T) {
	secret := testSecret32
	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
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
	secret := testSecret32
	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

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

func TestRequireXRequestedWith_AllowsMutationWithVerifiedAPIKey(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("POST", "/api/v1/queue/grab", nil)
	// Verified-API-key requests carry the AuthedViaAPIKey context flag set by
	// Middleware; RequireXRequestedWith exempts them on that flag.
	req = req.WithContext(WithAPIKeyAuth(req.Context()))
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("POST authenticated via verified API key must bypass RequireXRequestedWith")
	}
}

// TestRequireXRequestedWith_BogusAPIKeyDoesNotBypass is the #708 finding-3
// regression test for the X-Requested-With guard: a bogus ?apikey= must not
// exempt a mutating request from the X-Requested-With requirement.
func TestRequireXRequestedWith_BogusAPIKeyDoesNotBypass(t *testing.T) {
	called := false
	h := RequireXRequestedWith(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	// Bogus apikey, no verified-auth context flag, no X-Requested-With header.
	req, _ := http.NewRequest("DELETE", "/api/v1/queue/1?apikey=bogus-key", nil)
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("DELETE with bogus ?apikey= must NOT bypass RequireXRequestedWith (#708)")
	}
	if w.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.status)
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

// stackSecret32 is a 32-byte secret for CSRF stack tests.
var stackSecret32 = []byte("stack-secret-32-bytes-for-tests!")

func TestCSRFStack_APIKeyRequestPassesAllGuards(t *testing.T) {
	secret := stackSecret32
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
	secret := stackSecret32
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

// TestAPIKeyAuthGrantsAdminRole verifies that a request authenticated via a
// valid API key is granted admin role in the context, so RequireAdmin-protected
// endpoints are reachable without a session cookie.
// Regression test for Bug 11: API key auth bypasses CSRF but then fails at
// the role check with a misleading "admin role required" 403.
func TestAPIKeyAuthGrantsAdminRole(t *testing.T) {
	secret := stackSecret32
	p := &fakeProvider{mode: ModeEnabled, apiKey: "my-api-key", secret: secret}

	called := false
	stack := Middleware(p)(
		RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})),
	)

	req, _ := http.NewRequest("POST", "/api/v1/settings/auth/mode", nil)
	req.Header.Set("X-Api-Key", "my-api-key")
	rw := &captureWriter{}
	stack.ServeHTTP(rw, req)
	if !called {
		t.Fatalf("API-key POST to admin-protected endpoint must reach the handler; got status %d", rw.status)
	}
}

// TestAPIKeyAuthRoleVisibleInContext verifies that the admin role is present in
// the request context when API key auth is used, so handlers can inspect it.
func TestAPIKeyAuthRoleVisibleInContext(t *testing.T) {
	secret := stackSecret32
	p := &fakeProvider{mode: ModeEnabled, apiKey: "my-api-key", secret: secret}

	var gotRole string
	stack := Middleware(p)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotRole = UserRoleFromContext(r.Context())
	}))

	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.Header.Set("X-Api-Key", "my-api-key")
	stack.ServeHTTP(nopWriter{}, req)
	if gotRole != "admin" {
		t.Errorf("role in context = %q; want \"admin\"", gotRole)
	}
}

func TestCSRFStack_BrowserSessionWithoutCSRFTokenIsRejected(t *testing.T) {
	secret := stackSecret32
	p := &fakeProvider{mode: ModeEnabled, apiKey: "harpoon-key", secret: secret}
	called := false
	stack := buildCSRFStack(p, secret, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))

	// Browser session cookie present but no CSRF token — CSRF attack scenario.
	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
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

// --- #708 finding 3: bogus ?apikey= must not switch CSRF off (full stack) ----
//
// Exercises the real middleware chain (Middleware → RequireXRequestedWith →
// RequireCSRFToken) to prove that a request with a *bogus* apikey parameter is
// no longer exempted from the CSRF guards just because the parameter is
// present. Before the fix, requestAPIKey(r) != "" short-circuited both guards;
// the bogus key then failed verification in Middleware and the request
// authenticated via the session cookie — executing with CSRF fully disabled.

func TestCSRFStack_BogusAPIKeyWithSessionStillRequiresCSRF(t *testing.T) {
	secret := stackSecret32
	p := &fakeProvider{mode: ModeEnabled, apiKey: "the-real-key", secret: secret}
	called := false
	stack := buildCSRFStack(p, secret, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))

	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Attacker-style request: bogus apikey, victim's session cookie rides
	// along, browser header present, but NO CSRF token. The bogus key fails
	// subtle.ConstantTimeCompare, so the request authenticates via the cookie
	// and must be held to the CSRF token requirement.
	req, _ := http.NewRequest("POST", "/api/v1/queue/grab?apikey=bogus", nil)
	req.Header.Set("X-Requested-With", "bindery-ui")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	req.RemoteAddr = "8.8.8.8:12345"
	rw := &captureWriter{}
	stack.ServeHTTP(rw, req)
	if called {
		t.Fatal("bogus ?apikey= must not exempt a session-cookie POST from CSRF (#708)")
	}
	if rw.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", rw.status)
	}
}

func TestCSRFStack_BogusAPIKeyHeaderWithSessionStillRequiresXRW(t *testing.T) {
	secret := stackSecret32
	p := &fakeProvider{mode: ModeEnabled, apiKey: "the-real-key", secret: secret}
	called := false
	stack := buildCSRFStack(p, secret, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))

	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Bogus X-Api-Key header, session cookie, no X-Requested-With header.
	// RequireXRequestedWith must still reject it.
	req, _ := http.NewRequest("POST", "/api/v1/queue/grab", nil)
	req.Header.Set("X-Api-Key", "bogus")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	req.RemoteAddr = "8.8.8.8:12345"
	rw := &captureWriter{}
	stack.ServeHTTP(rw, req)
	if called {
		t.Fatal("bogus X-Api-Key must not exempt a session-cookie POST from X-Requested-With (#708)")
	}
	if rw.status != http.StatusForbidden {
		t.Errorf("status = %d; want 403", rw.status)
	}
}

// --- #708 finding 4a: ?apikey= accepted only for safe methods ---------------

// TestMiddleware_APIKeyInQueryRejectedForMutation verifies that a valid key
// supplied via the ?apikey= query parameter no longer authenticates a mutating
// request. The key must be sent in the X-Api-Key header for POST/PUT/DELETE so
// it does not leak into proxy logs, browser history, or Referer headers.
func TestMiddleware_APIKeyInQueryRejectedForMutation(t *testing.T) {
	p := &fakeProvider{mode: ModeEnabled, apiKey: "correct-key"}
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("POST", "/api/v1/author?apikey=correct-key", nil)
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("valid key via ?apikey= must NOT authenticate a POST (#708 finding 4a)")
	}
	if w.status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", w.status)
	}
}

// TestMiddleware_APIKeyInHeaderAcceptedForMutation is the companion: the header
// remains the supported way to authenticate a mutating request with a key.
func TestMiddleware_APIKeyInHeaderAcceptedForMutation(t *testing.T) {
	p := &fakeProvider{mode: ModeEnabled, apiKey: "correct-key"}
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("POST", "/api/v1/author", nil)
	req.Header.Set("X-Api-Key", "correct-key")
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("valid key via X-Api-Key header must authenticate a POST")
	}
}

// TestMiddleware_APIKeyInQueryAcceptedForSafeMethod confirms the documented
// read-only ?apikey= workflow (OPDS readers, GET integrations) still works.
func TestMiddleware_APIKeyInQueryAcceptedForSafeMethod(t *testing.T) {
	p := &fakeProvider{mode: ModeEnabled, apiKey: "correct-key"}
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author?apikey=correct-key", nil)
	h.ServeHTTP(nopWriter{}, req)
	if !called {
		t.Fatal("valid key via ?apikey= must still authenticate a GET")
	}
}

// TestMiddleware_VerifiedAPIKeySetsContextFlag verifies that a verified key
// sets the AuthedViaAPIKey context flag the CSRF guards depend on.
func TestMiddleware_VerifiedAPIKeySetsContextFlag(t *testing.T) {
	p := &fakeProvider{mode: ModeEnabled, apiKey: "correct-key"}
	mw := Middleware(p)
	var viaKey bool
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		viaKey = AuthedViaAPIKey(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.Header.Set("X-Api-Key", "correct-key")
	h.ServeHTTP(nopWriter{}, req)
	if !viaKey {
		t.Fatal("verified API-key request must set the AuthedViaAPIKey context flag")
	}
}

// TestMiddleware_SessionAuthDoesNotSetAPIKeyFlag verifies a session-cookie
// request never gets the API-key flag — otherwise it would skip CSRF.
func TestMiddleware_SessionAuthDoesNotSetAPIKeyFlag(t *testing.T) {
	secret := testSecret32
	p := &fakeProvider{mode: ModeEnabled, secret: secret}
	sessionVal, err := SignSession(secret, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	mw := Middleware(p)
	var viaKey bool
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		viaKey = AuthedViaAPIKey(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionVal})
	h.ServeHTTP(nopWriter{}, req)
	if viaKey {
		t.Fatal("session-cookie request must NOT set the AuthedViaAPIKey flag")
	}
}

// --- Finding 1: X-Forwarded-For spoofing of local-only mode -------------------

// mkReq builds a request whose true TCP peer is peer and which carries the
// given X-Forwarded-For header value (empty = no header). The true peer is
// stashed in context exactly as trustedProxyMiddleware does in production.
func mkReq(peer, xff string) *http.Request {
	r := &http.Request{RemoteAddr: peer, Header: http.Header{}}
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	r = r.WithContext(WithRealPeer(r.Context(), peer))
	return r
}

func cidrs(t *testing.T, raw string) []*net.IPNet {
	t.Helper()
	return ParseTrustedProxyCIDRs(raw)
}

func TestResolveClientIP_NoTrustedProxy_IgnoresXFF(t *testing.T) {
	// No trusted proxy: the TCP peer is the client; a forged XFF must be
	// ignored entirely. A remote client claiming a private IP stays remote.
	r := mkReq("8.8.8.8:443", "10.0.0.5")
	if got := ResolveClientIP(r, nil); got != "8.8.8.8" {
		t.Fatalf("ResolveClientIP = %q; want 8.8.8.8 (XFF must be ignored)", got)
	}
	if IsLocalRequestTrusted(r, nil) {
		t.Fatal("local-only must NOT be bypassed: forged XFF, no trusted proxy")
	}
}

func TestResolveClientIP_SpoofedLeftmostXFF_Rejected(t *testing.T) {
	// The core CVE: a remote client prepends a forged private IP, the trusted
	// proxy appends its own hop. chi RealIP would pick the leftmost (forged)
	// 10.0.0.5 — we must instead resolve to the real remote client.
	trusted := cidrs(t, "203.0.113.7") // the proxy
	r := mkReq("203.0.113.7:55000", "10.0.0.5, 8.8.8.8")
	if got := ResolveClientIP(r, trusted); got != "8.8.8.8" {
		t.Fatalf("ResolveClientIP = %q; want 8.8.8.8 (real client, not forged 10.0.0.5)", got)
	}
	if IsLocalRequestTrusted(r, trusted) {
		t.Fatal("local-only MUST NOT be bypassed by a forged leftmost XFF entry")
	}
}

func TestResolveClientIP_GenuineLocalClientViaProxy(t *testing.T) {
	// A genuine LAN client behind the trusted proxy: XFF has exactly one hop,
	// the real private client IP. local-only should allow it.
	trusted := cidrs(t, "203.0.113.7")
	r := mkReq("203.0.113.7:55000", "192.168.1.50")
	if got := ResolveClientIP(r, trusted); got != "192.168.1.50" {
		t.Fatalf("ResolveClientIP = %q; want 192.168.1.50", got)
	}
	if !IsLocalRequestTrusted(r, trusted) {
		t.Fatal("genuine LAN client behind trusted proxy should be local")
	}
}

func TestResolveClientIP_MultipleTrustedHopsPeeled(t *testing.T) {
	// Two chained trusted proxies. XFF: <client>, <proxyA>; TCP peer = proxyB.
	// Both proxies are trusted and must be peeled, leaving the client.
	trusted := cidrs(t, "203.0.113.7,203.0.113.8")
	r := mkReq("203.0.113.8:40000", "8.8.4.4, 203.0.113.7")
	if got := ResolveClientIP(r, trusted); got != "8.8.4.4" {
		t.Fatalf("ResolveClientIP = %q; want 8.8.4.4", got)
	}
	if IsLocalRequestTrusted(r, trusted) {
		t.Fatal("remote client behind two trusted proxies must not be local")
	}
}

func TestResolveClientIP_UntrustedPeerXFFIgnored(t *testing.T) {
	// The TCP peer is NOT a configured proxy: it talks to us directly, so it
	// is the client and any XFF it sends is untrusted.
	trusted := cidrs(t, "203.0.113.7")
	r := mkReq("8.8.8.8:1234", "10.0.0.9")
	if got := ResolveClientIP(r, trusted); got != "8.8.8.8" {
		t.Fatalf("ResolveClientIP = %q; want 8.8.8.8 (untrusted peer's XFF ignored)", got)
	}
	if IsLocalRequestTrusted(r, trusted) {
		t.Fatal("untrusted direct peer must not gain local via its own XFF")
	}
}

func TestResolveClientIP_IPv6TrustedProxyAndClient(t *testing.T) {
	// IPv6: trusted proxy is a /48; the real client is a public v6 address
	// prepended with a forged ULA. The forged fd00:: must not win.
	trusted := cidrs(t, "2001:db8:aaaa::/48")
	r := mkReq("[2001:db8:aaaa::1]:9000", "fd00::1, 2606:4700:4700::1111")
	if got := ResolveClientIP(r, trusted); got != "2606:4700:4700::1111" {
		t.Fatalf("ResolveClientIP = %q; want 2606:4700:4700::1111 (real v6 client)", got)
	}
	if IsLocalRequestTrusted(r, trusted) {
		t.Fatal("forged IPv6 ULA in XFF must not make the request local")
	}

	// Genuine IPv6 loopback client behind the trusted v6 proxy.
	r2 := mkReq("[2001:db8:aaaa::1]:9000", "::1")
	if !IsLocalRequestTrusted(r2, trusted) {
		t.Fatal("genuine ::1 client behind trusted v6 proxy should be local")
	}
}

func TestResolveClientIP_AllHopsTrusted(t *testing.T) {
	// Every XFF hop is itself a trusted proxy (degenerate config). The
	// outermost entry is taken as the best-effort client.
	trusted := cidrs(t, "203.0.113.0/24")
	r := mkReq("203.0.113.8:40000", "203.0.113.1, 203.0.113.2")
	if got := ResolveClientIP(r, trusted); got != "203.0.113.1" {
		t.Fatalf("ResolveClientIP = %q; want 203.0.113.1 (outermost)", got)
	}
}

func TestResolveClientIP_GarbageHopFailsClosed(t *testing.T) {
	// An unparseable XFF entry breaks the chain of trust; we must not keep
	// peeling past it (that could expose a forged private IP to its left).
	trusted := cidrs(t, "203.0.113.7")
	r := mkReq("203.0.113.7:55000", "10.0.0.5, garbage")
	got := ResolveClientIP(r, trusted)
	if got != "garbage" {
		t.Fatalf("ResolveClientIP = %q; want \"garbage\" (chain breaks, fail closed)", got)
	}
	if IsLocalRequestTrusted(r, trusted) {
		t.Fatal("garbage hop must not resolve to a local address")
	}
}

func TestIsLocalRequest_DirectLocalPeerNoXFF(t *testing.T) {
	// Direct LAN client, no proxy, no XFF — the common local-only case.
	r := mkReq("192.168.1.10:54321", "")
	if !IsLocalRequestTrusted(r, nil) {
		t.Fatal("direct LAN peer should be local")
	}
}

// --- Finding 3: session key-id and rotation primitive ------------------------

func TestSession_V2KeyIDRoundTrip(t *testing.T) {
	secret := testSecret32
	cookie, err := SignSession(secret, 99, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts := strings.Split(cookie, ".")
	if len(parts) != 5 || parts[0] != "v2" {
		t.Fatalf("cookie = %q; want 5-part v2 format", cookie)
	}
	if parts[1] != keyID(secret) {
		t.Errorf("cookie key-id = %q; want %q", parts[1], keyID(secret))
	}
	uid, err := VerifySession(secret, cookie)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if uid != 99 {
		t.Errorf("uid = %d; want 99", uid)
	}
}

func TestSession_KeyIDChangesWithSecret(t *testing.T) {
	a := []byte("secret-A-for-keyid-test-32bytes!!")
	b := []byte("secret-B-for-keyid-test-32bytes!!")
	if keyID(a) == keyID(b) {
		t.Fatal("different secrets must produce different key-ids")
	}
}

func TestSession_VerifyMultiAcceptsOldAndNew(t *testing.T) {
	// Rotation window: cookies signed under the previous secret must still
	// verify when the verifier is handed {new, old}.
	oldSecret := []byte("old-rotation-secret-32-bytes-long")
	newSecret := []byte("new-rotation-secret-32-bytes-long")

	oldCookie, err := SignSession(oldSecret, 7, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign old: %v", err)
	}
	newCookie, err := SignSession(newSecret, 8, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign new: %v", err)
	}

	set := [][]byte{newSecret, oldSecret}
	if uid, err := VerifySessionMulti(set, oldCookie); err != nil || uid != 7 {
		t.Fatalf("old cookie under rotation set: uid=%d err=%v; want 7,nil", uid, err)
	}
	if uid, err := VerifySessionMulti(set, newCookie); err != nil || uid != 8 {
		t.Fatalf("new cookie under rotation set: uid=%d err=%v; want 8,nil", uid, err)
	}
	// After the window closes (only the new secret), the old cookie must fail.
	if _, err := VerifySessionMulti([][]byte{newSecret}, oldCookie); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("old cookie post-rotation: want ErrSessionInvalid, got %v", err)
	}
}

func TestSession_LegacyV1StillVerifies(t *testing.T) {
	// A v1 cookie minted the old way must still verify so a deploy that adds
	// v2 does not invalidate every outstanding session at once.
	secret := testSecret32
	exp := time.Now().Add(time.Hour).Unix()
	payload := fmt.Sprintf("v1.%d.%d", int64(55), exp)
	mac := hmacSum(secret, payload)
	v1Cookie := payload + "." + base64.RawURLEncoding.EncodeToString(mac)

	uid, err := VerifySession(secret, v1Cookie)
	if err != nil {
		t.Fatalf("legacy v1 verify: %v", err)
	}
	if uid != 55 {
		t.Errorf("uid = %d; want 55", uid)
	}

	// A tampered v1 cookie must still be rejected.
	tampered := fmt.Sprintf("v1.%d.%d.", int64(9999), exp) +
		base64.RawURLEncoding.EncodeToString(mac)
	if _, err := VerifySession(secret, tampered); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("tampered v1 cookie: want ErrSessionInvalid, got %v", err)
	}
}

func TestSession_V2WrongKeyIDStillVerifiesWithCorrectSecret(t *testing.T) {
	// Decode-tolerant: a v2 cookie whose key-id field does not match (e.g.
	// truncated/garbled) must still verify if the signature is genuine — the
	// HMAC is authoritative, the key-id is only a routing hint.
	secret := testSecret32
	cookie, err := SignSession(secret, 12, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts := strings.Split(cookie, ".")
	// Re-sign the payload but lie about the key-id field. The HMAC covers the
	// key-id, so we must recompute it for the doctored payload to be valid.
	parts[1] = "deadbeef"
	doctoredPayload := strings.Join(parts[:4], ".")
	parts[4] = base64.RawURLEncoding.EncodeToString(hmacSum(secret, doctoredPayload))
	doctored := strings.Join(parts, ".")

	uid, err := VerifySession(secret, doctored)
	if err != nil {
		t.Fatalf("v2 with non-matching key-id but valid sig: %v", err)
	}
	if uid != 12 {
		t.Errorf("uid = %d; want 12", uid)
	}
}

func TestSession_VerifyMultiRejectsWhenNoSecretUsable(t *testing.T) {
	cookie, _ := SignSession(testSecret32, 1, time.Now().Add(time.Hour))
	if _, err := VerifySessionMulti([][]byte{[]byte("short"), nil}, cookie); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("VerifySessionMulti must fail closed when no secret meets minSecretLen")
	}
}
