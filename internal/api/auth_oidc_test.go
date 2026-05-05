package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth/oidc"
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
		OK             bool                 `json:"ok"`
		IssuerMismatch bool                 `json:"issuer_mismatch"`
		Discovered     *oidc.DiscoveryDoc   `json:"discovered"`
		Error          string               `json:"error"`
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
