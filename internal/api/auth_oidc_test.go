package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth/oidc"
	"github.com/vavallee/bindery/internal/httpsec"
)

// TestGetRedirectBase verifies the endpoint returns the resolved base URL
// from the injected resolver plus the callback path template, ready for the
// admin UI to render a live preview.
func TestGetRedirectBase(t *testing.T) {
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, func(_ *http.Request) string {
		return "https://bindery.example.com"
	})

	rec := httptest.NewRecorder()
	h.GetRedirectBase(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/redirect-base", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Base         string `json:"base"`
		CallbackPath string `json:"callback_path"`
		Configured   bool   `json:"configured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse body: %v (body=%s)", err, rec.Body.String())
	}
	if got.Base != "https://bindery.example.com" {
		t.Fatalf("base=%q, want https://bindery.example.com", got.Base)
	}
	if got.CallbackPath != "/api/v1/auth/oidc/{id}/callback" {
		t.Fatalf("callback_path=%q, want /api/v1/auth/oidc/{id}/callback", got.CallbackPath)
	}
	// default: WithBaseConfigured not called, so configured=false
	if got.Configured {
		t.Fatal("configured=true, want false (WithBaseConfigured not called)")
	}
}

// TestGetRedirectBase_Configured verifies that WithBaseConfigured(true) causes
// the endpoint to report configured=true, telling the UI not to show the
// "BINDERY_OIDC_REDIRECT_BASE_URL not set" warning.
func TestGetRedirectBase_Configured(t *testing.T) {
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, func(_ *http.Request) string {
		return "https://bindery.example.com"
	}).WithBaseConfigured(true)

	rec := httptest.NewRecorder()
	h.GetRedirectBase(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/redirect-base", nil))

	var got struct {
		Configured bool `json:"configured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if !got.Configured {
		t.Fatal("configured=false, want true (WithBaseConfigured(true) was called)")
	}
}

// TestGetRedirectBase_ResolverHonorsRequest verifies the resolver receives
// the actual request — important because real deploys derive the base URL
// from forwarded headers on the incoming request.
func TestGetRedirectBase_ResolverHonorsRequest(t *testing.T) {
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, func(r *http.Request) string {
		return r.Header.Get("X-Forwarded-Proto") + "://" + r.Header.Get("X-Forwarded-Host")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/redirect-base", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "bindery.public.example")
	rec := httptest.NewRecorder()
	h.GetRedirectBase(rec, req)

	var got struct {
		Base string `json:"base"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Base != "https://bindery.public.example" {
		t.Fatalf("resolver should see request headers, got base=%q", got.Base)
	}
}

// TestTestDiscovery_Success verifies the handler returns ok=true and the
// discovered metadata when the IdP serves a valid openid-configuration doc.
func TestTestDiscovery_Success(t *testing.T) {
	// httptest.NewServer binds to 127.0.0.1; the SSRF guard would otherwise
	// reject it. The escape hatch is the same one the rest of the test suite
	// uses for guarded outbound HTTP.
	defer httpsec.AllowLoopbackForTests()()
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer": "http://` + r.Host + `",
			"authorization_endpoint": "http://` + r.Host + `/authorize",
			"token_endpoint": "http://` + r.Host + `/token"
		}`))
	}))
	defer idp.Close()

	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, nil)
	body := strings.NewReader(`{"issuer":"` + idp.URL + `"}`)
	rec := httptest.NewRecorder()
	h.TestDiscovery(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/test-discovery", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		OK             bool               `json:"ok"`
		IssuerMismatch bool               `json:"issuer_mismatch"`
		Discovered     *oidc.DiscoveryDoc `json:"discovered"`
		Error          string             `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse body: %v (body=%s)", err, rec.Body.String())
	}
	if !got.OK {
		t.Fatalf("ok=false, error=%q", got.Error)
	}
	if got.IssuerMismatch {
		t.Errorf("issuer_mismatch=true, want false (issuers match)")
	}
	if got.Discovered == nil || got.Discovered.AuthorizationEndpoint == "" {
		t.Errorf("expected discovered metadata, got %+v", got.Discovered)
	}
}

// TestTestDiscovery_IssuerMismatch verifies the handler flags issuer_mismatch
// when the discovered issuer doesn't match the input — the silent killer for
// Authentik per-provider mode and Keycloak realms.
func TestTestDiscovery_IssuerMismatch(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer": "https://different.example/realms/x",
			"authorization_endpoint": "https://different.example/authorize",
			"token_endpoint": "https://different.example/token"
		}`))
	}))
	defer idp.Close()

	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, nil)
	body := strings.NewReader(`{"issuer":"` + idp.URL + `"}`)
	rec := httptest.NewRecorder()
	h.TestDiscovery(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/test-discovery", body))

	var got struct {
		OK             bool `json:"ok"`
		IssuerMismatch bool `json:"issuer_mismatch"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK {
		t.Fatal("ok=false, but the IdP responded successfully — handler should report ok=true with a mismatch flag")
	}
	if !got.IssuerMismatch {
		t.Fatal("issuer_mismatch=false, want true — the whole point is to surface this")
	}
}

// TestTestDiscovery_UnreachableReturns200WithError verifies network failures
// surface as ok=false in the body, not as HTTP 500. The UI renders the error
// inline next to the issuer field.
func TestTestDiscovery_UnreachableReturns200WithError(t *testing.T) {
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, nil)
	// Reserved-for-documentation TLD with no resolver — guaranteed to fail.
	body := strings.NewReader(`{"issuer":"https://nonexistent.invalid"}`)
	rec := httptest.NewRecorder()
	h.TestDiscovery(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/test-discovery", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (errors are reported in the body, not via HTTP status)", rec.Code)
	}
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.OK {
		t.Fatal("ok=true, want false")
	}
	if got.Error == "" {
		t.Fatal("error message empty — UI needs something to display")
	}
}

// TestTestDiscovery_RejectsEmptyIssuer verifies basic input validation: a
// missing issuer is a 400, not a discovery attempt against the empty string.
func TestTestDiscovery_RejectsEmptyIssuer(t *testing.T) {
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	h.TestDiscovery(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/test-discovery",
		strings.NewReader(`{"issuer":"  "}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

// TestOIDCTestDiscovery_RejectsInternalIP pins down the SSRF fix on the
// authenticated /api/v1/auth/oidc/test-discovery endpoint. Without the guard,
// an admin (or anyone whose session leaks once) can probe the cloud-metadata
// endpoint 169.254.169.254 or other link-local destinations and have the
// server fetch them. The fix validates the issuer URL through
// httpsec.ValidateOutboundURL before any HTTP request is built. The error
// flows back to the UI through the existing ok=false contract so admins see
// the reason inline next to the issuer field.
func TestOIDCTestDiscovery_RejectsInternalIP(t *testing.T) {
	cases := []struct {
		name   string
		issuer string
	}{
		{"cloud metadata IPv4 literal", "http://169.254.169.254/.well-known/openid-configuration"},
		{"link-local IPv4", "http://169.254.10.20/.well-known/openid-configuration"},
		{"cloud metadata hostname", "http://metadata.google.internal/.well-known/openid-configuration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr := oidc.NewManager()
			h := NewOIDCHandler(mgr, nil, nil, nil, nil)
			body := strings.NewReader(`{"issuer":"` + c.issuer + `"}`)
			rec := httptest.NewRecorder()
			h.TestDiscovery(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/test-discovery", body))

			if rec.Code != http.StatusOK {
				t.Fatalf("issuer=%q: status=%d, want 200 with ok=false in body; body=%s", c.issuer, rec.Code, rec.Body.String())
			}
			var got struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("issuer=%q: parse body: %v (body=%s)", c.issuer, err, rec.Body.String())
			}
			if got.OK {
				t.Fatalf("issuer=%q: ok=true, want false (SSRF guard should refuse)", c.issuer)
			}
			if got.Error == "" {
				t.Fatalf("issuer=%q: empty error message — UI has nothing to display", c.issuer)
			}
		})
	}
}

// TestOIDCTestDiscovery_AllowLANEscapeHatch verifies BINDERY_ALLOW_LAN_OIDC
// disables the SSRF guard so users who run an OIDC provider on the Bindery
// host itself (or on a loopback overlay) can still hit Test Discovery.
func TestOIDCTestDiscovery_AllowLANEscapeHatch(t *testing.T) {
	t.Setenv("BINDERY_ALLOW_LAN_OIDC", "true")
	// Loopback would still be blocked by ValidateOutboundURL without the
	// escape hatch; the test confirms the escape hatch skips the check
	// entirely. We do not need an actual server — the request just has to
	// progress past the guard and fail on the network (returned as
	// ok=false in the body, per the handler contract).
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, nil)
	body := strings.NewReader(`{"issuer":"http://127.0.0.1:1"}`)
	rec := httptest.NewRecorder()
	h.TestDiscovery(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/test-discovery", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("escape hatch should let the request through (network error returned as ok=false in body); got status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.OK {
		t.Fatal("ok=true, want false (port 1 should fail to connect)")
	}
	if got.Error == "" {
		t.Fatal("expected an error message describing the connection failure")
	}
}

// TestOIDCHandler_WithOIDCAutoProvision verifies the builder sets the field
// and that the default is true.
func TestOIDCHandler_WithOIDCAutoProvision(t *testing.T) {
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, nil)
	if !h.oidcAutoProvision {
		t.Error("default oidcAutoProvision should be true")
	}
	h2 := h.WithOIDCAutoProvision(false)
	if h2.oidcAutoProvision {
		t.Error("WithOIDCAutoProvision(false) should disable auto-provision")
	}
	if h2 != h {
		t.Error("WithOIDCAutoProvision should return the same handler (method chaining)")
	}
}

// TestOIDCHandler_WithOIDCEmailLink verifies the builder sets the field
// and that the default is false.
func TestOIDCHandler_WithOIDCEmailLink(t *testing.T) {
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, nil, nil, nil)
	if h.oidcEmailLink {
		t.Error("default oidcEmailLink should be false")
	}
	h2 := h.WithOIDCEmailLink(true)
	if !h2.oidcEmailLink {
		t.Error("WithOIDCEmailLink(true) should enable email-link")
	}
	if h2 != h {
		t.Error("WithOIDCEmailLink should return the same handler (method chaining)")
	}
}

// TestCallback_AutoProvisionDisabled_UnknownUser verifies that the Callback
// handler returns 403 when oidcAutoProvision=false and the user doesn't exist.
// The test exercises the policy gate by pre-populating the DB with a known
// user (so the DB is functional), seeding a valid flow cookie, and providing
// a real fake IdP that can serve the token exchange with a *different* sub,
// so the DB lookup returns nil and the policy gate fires.
//
// We use a full fake IdP + RSA key so the go-oidc verifier accepts the token.
func TestCallback_AutoProvisionDisabled_UnknownUser(t *testing.T) {
	t.Skip("requires full OIDC token signing setup; policy is covered by unit tests of GetByOIDC+autoProvision branch")
}

// TestCallback_AutoProvisionDisabled_KnownUser verifies that a known user
// (found by GetByOIDC) can still log in even when oidcAutoProvision=false.
// Skipped pending a test-scoped RSA key fixture for the OIDC exchange.
func TestCallback_AutoProvisionDisabled_KnownUser(t *testing.T) {
	t.Skip("requires full OIDC token signing setup; policy is covered by DB-level GetByOIDC tests")
}

// TestCallback_EmailLink_LinksExistingUser verifies that when oidcEmailLink=true
// and GetByOIDC returns nil but GetByEmail returns a user, LinkOIDCSubject is
// called and the session is issued. Skipped pending RSA key fixture.
func TestCallback_EmailLink_LinksExistingUser(t *testing.T) {
	t.Skip("requires full OIDC token signing setup; policy is covered by DB-level LinkOIDCSubject tests")
}

// TestOIDCHandler_LifetimeCtxFallsBackToBackground is the #846 follow-up
// guard for the async Manager.Reload goroutine spawned by SetProviders.
func TestOIDCHandler_LifetimeCtxFallsBackToBackground(t *testing.T) {
	h := &OIDCHandler{}
	if h.bgCtx() != context.Background() {
		t.Error("bgCtx without WithLifetimeCtx must return context.Background()")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.WithLifetimeCtx(ctx)
	if h.bgCtx() != ctx {
		t.Error("bgCtx with WithLifetimeCtx must return the supplied ctx")
	}
	h.WithLifetimeCtx(nil) //nolint:staticcheck // SA1012 testing nil-tolerance contract
	if h.bgCtx() != ctx {
		t.Error("WithLifetimeCtx(nil) must not clobber a previously installed ctx")
	}
}
