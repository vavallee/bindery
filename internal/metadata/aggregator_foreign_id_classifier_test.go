package metadata

import (
	"context"
	"testing"
)

// TestProviderNameForForeignIDMatchesModels is the #2352 regression guard. This
// classifier is one of nine copies of the same prefix routing and it had
// drifted to four branches, so a Calibre or Audiobookshelf synthetic ID fell
// through to the openlibrary default.
func TestProviderNameForForeignIDMatchesModels(t *testing.T) {
	cases := map[string]string{
		"calibre:author:5": "calibre",
		"abs:author:abc":   "audiobookshelf",
		"hc:brandon":       "hardcover",
		"gb:zyTCAlFPjgYC":  "googlebooks",
		"dnb:118540238":    "dnb",
		"OL26320A":         "openlibrary",
		"":                 "openlibrary",
	}
	for id, want := range cases {
		if got := providerNameForForeignID(id); got != want {
			t.Errorf("providerNameForForeignID(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestProviderForForeignIDRefusesImporterIDs pins the consequence of the fix:
// no configured provider owns a synthetic importer prefix, so routing returns
// nil instead of handing the ID to OpenLibrary, which 404s on it. Every caller
// of providerForForeignID already handles nil.
func TestProviderForForeignIDRefusesImporterIDs(t *testing.T) {
	ol := &mockProvider{name: "openlibrary"}
	hc := &mockProvider{name: "hardcover"}
	agg := newTestAggregator(ol, hc)

	for _, id := range []string{"calibre:author:5", "abs:author:abc"} {
		if provider := agg.providerForForeignID(id); provider != nil {
			t.Errorf("providerForForeignID(%q) routed to %q, want nil", id, providerName(provider))
		}
	}

	// Real provider IDs and a bare OpenLibrary key are untouched.
	if got := providerName(agg.providerForForeignID("OL26320A")); got != "openlibrary" {
		t.Errorf("providerForForeignID(bare OL id) = %q, want openlibrary", got)
	}
	if got := providerName(agg.providerForForeignID("hc:brandon")); got != "hardcover" {
		t.Errorf("providerForForeignID(hc: id) = %q, want hardcover", got)
	}

	// The user-facing consequence: GetAuthor answers (nil, nil) for a Calibre
	// stub instead of spending a round trip that can only 404.
	author, err := agg.GetAuthor(context.Background(), "calibre:author:5")
	if err != nil || author != nil {
		t.Errorf("GetAuthor(calibre stub) = (%v, %v), want (nil, nil)", author, err)
	}
}

// TestSafeToBindUnchangedForProviderIDs pins that the classifier swap does not
// move SafeToBind for any ID a provider search can actually return. Provider
// search never yields a calibre: or abs: prefix, so those cases stay
// theoretical, but they now classify as themselves rather than as openlibrary.
func TestSafeToBindUnchangedForProviderIDs(t *testing.T) {
	// Nothing failed: everything binds regardless of prefix.
	clean := SearchOutcome{Primary: "openlibrary"}
	for _, id := range []string{"hc:brandon", "gb:zyTCAlFPjgYC", "dnb:118540238", "OL26320A", "calibre:author:5"} {
		if !clean.SafeToBind(id) {
			t.Errorf("SafeToBind(%q) = false with no primary failure, want true", id)
		}
	}

	// The primary failed, so only a match that came from the primary binds.
	failed := SearchOutcome{Primary: "openlibrary", PrimaryFailed: true, FailedProviders: []string{"openlibrary"}}
	if !failed.SafeToBind("OL26320A") {
		t.Error("SafeToBind(bare OL id) = false, want true: the match came from the primary")
	}
	for _, id := range []string{"hc:brandon", "gb:zyTCAlFPjgYC", "dnb:118540238"} {
		if failed.SafeToBind(id) {
			t.Errorf("SafeToBind(%q) = true after a primary failure, want false", id)
		}
	}
}
