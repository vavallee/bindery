package useragent

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildFormats(t *testing.T) {
	suffix := " (" + runtime.GOOS + "; " + ContactURL + ")"
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
	original := Get()
	t.Cleanup(func() {
		// Restore — other parallel tests may observe Get().
		suffix := " (" + runtime.GOOS + "; " + ContactURL + ")"
		v := strings.TrimSuffix(strings.TrimPrefix(original, "bindery/"), suffix)
		Set(strings.TrimPrefix(v, "v"))
	})

	Set("9.9.9")
	if got, want := Get(), "bindery/9.9.9 ("+runtime.GOOS+"; "+ContactURL+")"; got != want {
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
	ua := Build("1.0.0")
	if !strings.Contains(ua, ContactURL) {
		t.Fatalf("User-Agent must contain the configured contact pointer %q; got %q", ContactURL, ua)
	}
	if !strings.Contains(ua, "github.com") && !strings.Contains(ua, "@") {
		t.Fatalf("User-Agent must contain a URL or email contact; got %q", ua)
	}
}
