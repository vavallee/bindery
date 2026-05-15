package useragent

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildFormats(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"semver", "1.11.1", "bindery/1.11.1 (" + runtime.GOOS + ")"},
		{"v-prefix stripped", "v1.11.1", "bindery/1.11.1 (" + runtime.GOOS + ")"},
		{"whitespace trimmed", "  1.11.1  ", "bindery/1.11.1 (" + runtime.GOOS + ")"},
		{"empty falls back to dev", "", "bindery/dev (" + runtime.GOOS + ")"},
		{"only spaces falls back to dev", "   ", "bindery/dev (" + runtime.GOOS + ")"},
		{"dev passes through", "dev", "bindery/dev (" + runtime.GOOS + ")"},
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
		Set(strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(original, "bindery/"), " ("+runtime.GOOS+")"), "v"))
	})

	Set("9.9.9")
	if got, want := Get(), "bindery/9.9.9 ("+runtime.GOOS+")"; got != want {
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
