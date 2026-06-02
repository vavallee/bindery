package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
)

// DiscoverOption configures the discovery client. Currently only DiscoverPolicy
// is exposed; the option pattern keeps Discover's signature stable as more
// guardrails are layered in.
type DiscoverOption func(*discoverConfig)

// discoverConfig holds the resolved Discover options. policy is a pointer so
// the absence of a policy (zero value) is distinct from PolicyLAN (the
// strictest-by-default policy that still admits RFC1918 IdPs).
type discoverConfig struct {
	policy       *httpsec.Policy
	maxRedirects int
}

// DiscoverPolicy installs an httpsec.Policy that is re-applied to every
// redirect hop. Without this option, redirects are followed without
// per-hop validation — appropriate only when the caller has no SSRF
// concerns (e.g. an in-process test against httptest.NewServer).
func DiscoverPolicy(p httpsec.Policy) DiscoverOption {
	return func(c *discoverConfig) { c.policy = &p }
}

// DiscoverMaxRedirects overrides the default redirect cap (5). Useful for
// tests that need to exercise the cap without flooding production with a
// long-running redirect chain.
func DiscoverMaxRedirects(n int) DiscoverOption {
	return func(c *discoverConfig) {
		if n > 0 {
			c.maxRedirects = n
		}
	}
}

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
// deadline via the supplied context. When opts contains a DiscoverPolicy,
// every redirect hop is re-validated against the supplied httpsec.Policy so
// an attacker cannot bounce the request from a public issuer URL to a
// private destination (e.g. http://169.254.169.254/) the initial URL guard
// would otherwise have refused.
func Discover(ctx context.Context, issuer string, opts ...DiscoverOption) (*DiscoveryDoc, error) {
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

	cfg := discoverConfig{maxRedirects: 5}
	for _, opt := range opts {
		opt(&cfg)
	}

	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Cap hop count to prevent redirect-loop / blow-up abuse. Default
			// 5 mirrors Go's stdlib default, but we set it explicitly so the
			// cap survives any future stdlib change.
			if len(via) >= cfg.maxRedirects {
				return fmt.Errorf("stopped after %d redirects", cfg.maxRedirects)
			}
			// When the caller installed an SSRF policy, re-validate every hop's
			// destination. This defeats the redirect-bypass where an attacker
			// controls a public-looking issuer URL that 302s to
			// http://169.254.169.254/ (or any internal address the initial-URL
			// guard refuses), bouncing the request past the perimeter check.
			if cfg.policy != nil {
				if err := httpsec.ValidateOutboundURL(req.URL.String(), *cfg.policy); err != nil {
					return fmt.Errorf("redirect refused: %w", err)
				}
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		// http.Client.Do wraps the CheckRedirect error in a *url.Error; surface
		// the underlying redirect-refusal message so callers can log it cleanly.
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Err != nil {
			return nil, uerr.Err
		}
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
