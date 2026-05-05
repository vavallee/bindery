package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DiscoveryDoc is the subset of the OIDC provider metadata document that the
// Settings UI's "Test discovery" button surfaces. The fields here match the
// OpenID Connect Discovery 1.0 spec (§4.2) — only the ones admins need to see
// to confirm the IdP is reachable and configured for the issuer they entered.
type DiscoveryDoc struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint,omitempty"`
	JWKSUri               string   `json:"jwks_uri,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
}

// Discover fetches <issuer>/.well-known/openid-configuration and returns the
// parsed metadata. It does NOT validate the JWKS or run a verifier — this is
// strictly a "is the IdP reachable and does it speak OIDC discovery?" probe.
//
// The discovered Issuer field is intentionally returned verbatim: a value
// different from the input is the silent killer for Authentik per-provider
// mode (where the issuer URL has the application slug appended) and Keycloak
// realm paths. The caller should compare and warn.
//
// SSRF guardrails: only http and https schemes are accepted; redirects are
// limited; response body is capped at 256 KB; total operation has its own
// deadline via the supplied context.
func Discover(ctx context.Context, issuer string) (*DiscoveryDoc, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return nil, fmt.Errorf("empty issuer")
	}
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("parse issuer: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("issuer must use http or https scheme")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("issuer must include a host")
	}

	wellKnown := issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery endpoint returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return nil, fmt.Errorf("read discovery body: %w", err)
	}
	var doc DiscoveryDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse discovery doc: %w", err)
	}
	if doc.Issuer == "" {
		return nil, fmt.Errorf("discovery doc missing issuer field")
	}
	return &doc, nil
}
