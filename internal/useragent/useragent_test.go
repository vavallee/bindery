package useragent

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildFormats(t *testing.T) {
	t.Setenv("BINDERY_CONTACT", "")
	suffix := " (" + runtime.GOOS + "; " + DefaultContactURL + ")"
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"semver", "1.11.1", "bindery/1.11.1" + suffix},
		{"v-prefix stripped", "v1.11.1", "bindery/1.11.1" + suffix},
		{"whitespace trimmed", "  1.11.1  ", "bindery/1.11.1" + suffix},
		{"empty falls back to dev", "", "bindery/dev" + suffix},
		{"only spaces falls back to dev", "   ", "bindery/dev" + suffix},
		{"dev passes through", "dev", "bindery/dev" + suffix},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Build(tc.version); got != tc.want {
				t.Fatalf("Build(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}

func TestSetGet(t *testing.T) {
	t.Setenv("BINDERY_CONTACT", "")
	original := Get()
	t.Cleanup(func() {
		// Restore — other parallel tests may observe Get().
		suffix := " (" + runtime.GOOS + "; " + DefaultContactURL + ")"
		v := strings.TrimSuffix(strings.TrimPrefix(original, "bindery/"), suffix)
		Set(strings.TrimPrefix(v, "v"))
	})

	Set("9.9.9")
	if got, want := Get(), "bindery/9.9.9 ("+runtime.GOOS+"; "+DefaultContactURL+")"; got != want {
		t.Fatalf("Get() = %q, want %q", got, want)
	}
}

func TestUALowercaseB(t *testing.T) {
	// Regression guard: nzbfinder.ws WAF blocks any UA containing capital-B
	// "Bindery". The whole point of this package is to keep us lowercase.
	if ua := Get(); strings.Contains(ua, "Bindery") {
		t.Fatalf("User-Agent must not contain capital-B 'Bindery'; got %q", ua)
	}
}

// TestUAContainsContactPointer is the #834 regression guard: OpenLibrary
// returns 403 on every search when the User-Agent has no contact pointer
// (email or URL). Asserting a URL or email is present in the built UA
// stops a future refactor from silently re-breaking name/title searches.
func TestUAContainsContactPointer(t *testing.T) {
	t.Setenv("BINDERY_CONTACT", "")
	ua := Build("1.0.0")
	if !strings.Contains(ua, DefaultContactURL) {
		t.Fatalf("User-Agent must contain the default contact pointer %q; got %q", DefaultContactURL, ua)
	}
	if !strings.Contains(ua, "github.com") && !strings.Contains(ua, "@") {
		t.Fatalf("User-Agent must contain a URL or email contact; got %q", ua)
	}
}

// TestUAHonorsBinderyContactEnv is the #848 fix: operators hitting per-UA
// rate-limiting on shared fleets must be able to set their own contact so
// each install presents a distinct identity to upstream providers.
func TestUAHonorsBinderyContactEnv(t *testing.T) {
	t.Run("explicit mailto", func(t *testing.T) {
		t.Setenv("BINDERY_CONTACT", "mailto:alice@example.org")
		ua := Build("1.0.0")
		if !strings.Contains(ua, "mailto:alice@example.org") {
			t.Errorf("UA missing mailto contact: %q", ua)
		}
		if strings.Contains(ua, DefaultContactURL) {
			t.Errorf("UA must not contain default contact when override set: %q", ua)
		}
	})
	t.Run("bare email gets mailto prefix", func(t *testing.T) {
		t.Setenv("BINDERY_CONTACT", "bob@example.org")
		ua := Build("1.0.0")
		if !strings.Contains(ua, "mailto:bob@example.org") {
			t.Errorf("UA missing mailto-prefixed bare email: %q", ua)
		}
	})
	t.Run("https URL passes through", func(t *testing.T) {
		t.Setenv("BINDERY_CONTACT", "https://my-bindery.example.org")
		ua := Build("1.0.0")
		if !strings.Contains(ua, "https://my-bindery.example.org") {
			t.Errorf("UA missing override URL: %q", ua)
		}
	})
	t.Run("whitespace inside the value is stripped", func(t *testing.T) {
		t.Setenv("BINDERY_CONTACT", "  carol @ example.org  ")
		ua := Build("1.0.0")
		if !strings.Contains(ua, "mailto:carol@example.org") {
			t.Errorf("UA must strip whitespace before mailto-prefixing: %q", ua)
		}
	})
	t.Run("empty falls back to default", func(t *testing.T) {
		t.Setenv("BINDERY_CONTACT", "")
		ua := Build("1.0.0")
		if !strings.Contains(ua, DefaultContactURL) {
			t.Errorf("empty BINDERY_CONTACT should yield default URL: %q", ua)
		}
	})
}
