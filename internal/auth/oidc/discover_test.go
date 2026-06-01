package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
)

// TestDiscover_Success verifies the happy path returns parsed metadata when
// the IdP serves a well-formed openid-configuration document.
func TestDiscover_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer": "` + serverIssuer(r) + `",
			"authorization_endpoint": "` + serverIssuer(r) + `/authorize",
			"token_endpoint": "` + serverIssuer(r) + `/token",
			"jwks_uri": "` + serverIssuer(r) + `/jwks",
			"scopes_supported": ["openid","email","profile"]
		}`))
	}))
	defer srv.Close()

	doc, err := Discover(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if doc.Issuer != srv.URL {
		t.Errorf("Issuer=%q, want %q", doc.Issuer, srv.URL)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" {
		t.Errorf("expected auth/token endpoints populated: %+v", doc)
	}
	if len(doc.ScopesSupported) != 3 {
		t.Errorf("scopes=%v, want 3 entries", doc.ScopesSupported)
	}
}

// TestDiscover_IssuerMismatch verifies the function returns the discovered
// issuer verbatim — the caller is responsible for comparing against the
// requested issuer and surfacing the mismatch. This is the silent killer
// for Authentik per-provider mode and Keycloak realm paths.
func TestDiscover_IssuerMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer": "https://different.example/realms/x",
			"authorization_endpoint": "https://different.example/authorize",
			"token_endpoint": "https://different.example/token"
		}`))
	}))
	defer srv.Close()

	doc, err := Discover(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if doc.Issuer == srv.URL {
		t.Errorf("expected discovered issuer to differ from input — that's the whole point of the test")
	}
	if doc.Issuer != "https://different.example/realms/x" {
		t.Errorf("Issuer=%q, want the value the server returned", doc.Issuer)
	}
}

// TestDiscover_NonOK verifies a non-2xx response is surfaced as an error so
// the UI can show "discovery returned 404 Not Found" instead of a generic
// downstream login failure.
func TestDiscover_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := Discover(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 404 response")
		return
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error=%q, want it to mention 404 status", err.Error())
	}
}

// TestDiscover_RejectsNonHTTPScheme guards against SSRF tricks that might
// abuse file:// or gopher:// schemes. http and https only.
func TestDiscover_RejectsNonHTTPScheme(t *testing.T) {
	for _, bad := range []string{"file:///etc/passwd", "gopher://internal", "ftp://idp"} {
		_, err := Discover(context.Background(), bad)
		if err == nil {
			t.Errorf("Discover(%q) should fail, got nil", bad)
		}
	}
}

// TestDiscover_RejectsEmptyHost guards against schemes-only inputs that
// might bypass the scheme check downstream.
func TestDiscover_RejectsEmptyHost(t *testing.T) {
	_, err := Discover(context.Background(), "https://")
	if err == nil {
		t.Fatal("expected error for empty-host issuer")
	}
}

// TestDiscover_RespectsContext verifies a cancelled context aborts the call
// rather than waiting on the upstream HTTP timeout.
func TestDiscover_RespectsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Discover(ctx, srv.URL)
	if err == nil {
		t.Fatal("expected context-deadline error, got nil")
	}
}

// TestDiscover_TrimTrailingSlash verifies the well-known URL is constructed
// without a double slash when the user enters "https://idp.example.com/".
func TestDiscover_TrimTrailingSlash(t *testing.T) {
	var seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"x","authorization_endpoint":"a","token_endpoint":"t"}`))
	}))
	defer srv.Close()

	_, _ = Discover(context.Background(), srv.URL+"/")
	if seenPath != "/.well-known/openid-configuration" {
		t.Errorf("path=%q, want exactly /.well-known/openid-configuration (no double slash)", seenPath)
	}
}

// TestDiscover_RedirectToInternalIP_IsBlocked verifies the CheckRedirect hook
// re-validates every hop against the supplied policy. Without it, an attacker
// who controls a public-looking issuer URL could 302 the discovery probe to
// http://10.0.0.1/ (or http://169.254.169.254/) and bypass the initial URL
// guard at the API handler.
func TestDiscover_RedirectToInternalIP_IsBlocked(t *testing.T) {
	// httptest.NewServer binds to 127.0.0.1, which the SSRF guard rejects.
	// Allow loopback so we can run the public-looking origin server; the
	// redirect destination (10.0.0.1) is the one that has to be refused.
	defer httpsec.AllowLoopbackForTests()()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to an RFC1918 address — under PolicyStrict this is the
		// attack we are testing against. We use PolicyStrict in the call
		// below so the redirect-validation actually fires on 10.0.0.1.
		http.Redirect(w, r, "http://10.0.0.1/.well-known/openid-configuration", http.StatusFound)
	}))
	defer origin.Close()

	_, err := Discover(context.Background(), origin.URL, DiscoverPolicy(httpsec.PolicyStrict))
	if err == nil {
		t.Fatal("expected redirect to internal IP to be refused, got nil")
	}
	if !strings.Contains(err.Error(), "redirect refused") {
		t.Errorf("error=%q, want a redirect-refused message so operators can tell it apart from a transport error", err.Error())
	}
}

// TestDiscover_RedirectToCloudMetadata_IsBlocked is the same shape but
// against the cloud-metadata endpoint, which PolicyLAN already refuses (so
// callers don't need PolicyStrict to get the protection that matters most).
func TestDiscover_RedirectToCloudMetadata_IsBlocked(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/iam/security-credentials/", http.StatusFound)
	}))
	defer origin.Close()

	_, err := Discover(context.Background(), origin.URL, DiscoverPolicy(httpsec.PolicyLAN))
	if err == nil {
		t.Fatal("expected redirect to 169.254.169.254 to be refused, got nil")
	}
	if !strings.Contains(err.Error(), "redirect refused") {
		t.Errorf("error=%q, want a redirect-refused message", err.Error())
	}
}

// TestDiscover_RedirectCap stops a malicious or misconfigured IdP from
// chaining endless redirects.
func TestDiscover_RedirectCap(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Each hit redirects back to itself, growing via[] without bound.
		http.Redirect(w, r, srv.URL+"/.well-known/openid-configuration", http.StatusFound)
	}))
	defer srv.Close()

	_, err := Discover(context.Background(), srv.URL, DiscoverMaxRedirects(3))
	if err == nil {
		t.Fatal("expected redirect-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "stopped after") {
		t.Errorf("error=%q, want it to mention the redirect cap", err.Error())
	}
}

func serverIssuer(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
