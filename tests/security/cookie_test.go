package security_test

import (
	"os"
	"testing"

	"github.com/vavallee/bindery/internal/api"
)

// TestCookieSecureMode_DefaultAuto asserts the cookie Secure flag logic
// defaults to "auto" when the env var is unset. Auto means Secure flips
// on under TLS / X-Forwarded-Proto: https, off otherwise — the safe
// default for both homelab and reverse-proxy deployments.
func TestCookieSecureMode_DefaultAuto(t *testing.T) {
	t.Parallel()
	// SetenvForTest via os.Unsetenv; CI may have this set.
	prev, had := os.LookupEnv("BINDERY_COOKIE_SECURE")
	_ = os.Unsetenv("BINDERY_COOKIE_SECURE")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("BINDERY_COOKIE_SECURE", prev)
		}
	})
	if got := api.CookieSecureMode(); got != "auto" {
		t.Errorf("default mode: want %q, got %q", "auto", got)
	}
}

// TestCookieSecureMode_Overrides verifies the three documented env values
// are honored verbatim. Any other value falls back to auto (safe default).
func TestCookieSecureMode_Overrides(t *testing.T) {
	cases := map[string]string{
		"always":  "always",
		"never":   "never",
		"auto":    "auto",
		"unknown": "auto",
		"":        "auto",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Setenv("BINDERY_COOKIE_SECURE", in)
			if got := api.CookieSecureMode(); got != want {
				t.Errorf("BINDERY_COOKIE_SECURE=%q: want %q, got %q", in, want, got)
			}
		})
	}
}
