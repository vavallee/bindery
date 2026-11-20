package httpsec

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// NormalizeServiceBaseURL validates and canonicalises the base URL an admin
// types for a self-hosted service (Audiobookshelf, Grimmory). It requires an
// http or https scheme and a host, drops any query and fragment, and trims
// trailing slashes. service names the product in the missing-scheme message.
//
// It does not apply the outbound address policy, so clients built against an
// httptest server on loopback still work; settings-save boundaries use
// ValidateServiceBaseURL.
func NormalizeServiceBaseURL(raw, service string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("base_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("base_url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		if isSchemeless(u) {
			return "", fmt.Errorf("base_url %q is missing a scheme: write it as %q, or https:// if %s is behind TLS", raw, "http://"+raw, service)
		}
		return "", fmt.Errorf("base_url %q must use http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("base_url %q is missing a host", raw)
	}
	u.RawQuery = ""
	u.Fragment = ""
	if u.Path == "/" {
		u.Path = ""
	} else {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String(), nil
}

// ValidateServiceBaseURL is NormalizeServiceBaseURL plus the PolicyService
// outbound check, for the admin-input boundary (settings save): link-local and
// cloud-metadata endpoints are refused, loopback and RFC1918 are allowed for
// homelab deployments.
func ValidateServiceBaseURL(raw, service string) (string, error) {
	u, err := NormalizeServiceBaseURL(raw, service)
	if err != nil {
		return "", err
	}
	if err := ValidateOutboundURL(u, PolicyService); err != nil {
		return "", fmt.Errorf("base_url %q: %w", raw, err)
	}
	return u, nil
}

// NormalizeAPIKey trims an API key and refuses control characters, which
// would otherwise reach an outbound request header.
func NormalizeAPIKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("api_key contains invalid control characters")
		}
	}
	return key, nil
}

// isSchemeless reports whether raw was a bare host rather than a URL carrying an
// unsupported scheme, so the caller can say "you left off http://" instead of
// "that scheme is not allowed".
//
// The distinction matters because of how url.Parse reads a host:port pair with
// no scheme. "audiobookshelf:13378" parses as Scheme "audiobookshelf" with
// Opaque "13378", so the scheme check rejects it with a complaint about the
// scheme for input whose only visible novelty is the port. A user reading that
// reasonably concludes the port was refused and retries without it, landing on
// port 80 and a connection to the wrong service entirely (#2056).
//
// Deliberately does not repair the value. Choosing http over https on the
// user's behalf is a security relevant guess, so this names the corrected
// string and lets them confirm it.
func isSchemeless(u *url.URL) bool {
	// "audiobookshelf" or "audiobookshelf/abs": no scheme at all.
	if u.Scheme == "" {
		return true
	}
	// "audiobookshelf:13378": the port became the opaque part and no host was
	// found. Digits only, so a real unsupported scheme ("ftp://host") is not
	// caught here.
	return u.Host == "" && u.Opaque != "" && isAllDigits(u.Opaque)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
