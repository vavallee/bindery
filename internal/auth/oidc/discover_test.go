package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func serverIssuer(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
