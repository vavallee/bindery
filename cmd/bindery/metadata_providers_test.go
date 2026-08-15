package main

import "testing"

// TestResolveMetadataPrimaryProvider covers the boot-time provider selection:
// openlibrary stays the default for every unset / unusable value, dnb and
// hardcover are honoured when selected, and a tokenless hardcover selection
// degrades to openlibrary instead of leaving every lookup unauthenticated.
func TestResolveMetadataPrimaryProvider(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		hasToken   bool
		want       string
	}{
		{"unset defaults to openlibrary", "", false, "openlibrary"},
		{"unset defaults to openlibrary even with a token", "", true, "openlibrary"},
		{"explicit openlibrary", "openlibrary", false, "openlibrary"},
		{"explicit dnb", "dnb", false, "dnb"},
		{"hardcover with a token", "hardcover", true, "hardcover"},
		{"hardcover without a token falls back", "hardcover", false, "openlibrary"},
		{"unknown value falls back", "goodreads", true, "openlibrary"},
		{"enricher-only provider falls back", "googlebooks", true, "openlibrary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMetadataPrimaryProvider(tc.configured, tc.hasToken); got != tc.want {
				t.Errorf("resolveMetadataPrimaryProvider(%q, %v) = %q, want %q",
					tc.configured, tc.hasToken, got, tc.want)
			}
		})
	}
}
