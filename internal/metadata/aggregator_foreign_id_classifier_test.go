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
		"calibre:author:5":     "calibre",
		"abs:author:abc":       "audiobookshelf",
		"manual:book:17000000": "manual",
		"hc:brandon":           "hardcover",
		"gb:zyTCAlFPjgYC":      "googlebooks",
		"dnb:118540238":        "dnb",
		"nb:author:1":          "nb",
		"bnf:cb48780493c":      "bnf",
		"OL26320A":             "openlibrary",
		"":                     "openlibrary",
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

	for _, id := range []string{"calibre:author:5", "abs:author:abc", "manual:author:dan-abnett", "manual:book:17000000"} {
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

	// A book made by hand (#3840) is never sent to a provider either, which
	// is what keeps a refresh from replacing what the user typed.
	book, err := agg.GetBook(context.Background(), "manual:book:17000000")
	if err != nil || book != nil {
		t.Errorf("GetBook(manual book) = (%v, %v), want (nil, nil)", book, err)
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

// TestBnFForeignIDsRouteOnlyToBnF pins the "bnf:" prefix (#3144). Before the
// prefix was known it classified as openlibrary, so with OpenLibrary primary
// a bnf: author would have been handed to the OpenLibrary client, and
// SafeToBind would have treated a BnF match as the primary's after an
// OpenLibrary failure.
func TestBnFForeignIDsRouteOnlyToBnF(t *testing.T) {
	ol := &mockProvider{name: "openlibrary"}
	if got := newTestAggregator(ol).providerForForeignID("bnf:author:11885918"); got != nil {
		t.Errorf("bnf: id routed to %q with OpenLibrary primary, want nil", providerName(got))
	}
	bnf := &mockProvider{name: "bnf"}
	if got := providerName(newTestAggregator(bnf, ol).providerForForeignID("bnf:cb48780493c")); got != "bnf" {
		t.Errorf("bnf: id routed to %q with BnF primary, want bnf", got)
	}

	olFailed := SearchOutcome{Primary: "openlibrary", PrimaryFailed: true, FailedProviders: []string{"openlibrary"}}
	if olFailed.SafeToBind("bnf:author:11885918") {
		t.Error("SafeToBind(bnf:) = true after an OpenLibrary primary failure, want false")
	}
	bnfFailed := SearchOutcome{Primary: "bnf", PrimaryFailed: true, FailedProviders: []string{"bnf"}}
	if !bnfFailed.SafeToBind("bnf:author:11885918") {
		t.Error("SafeToBind(bnf:) = false with BnF primary, want true: the match came from the primary")
	}
	if bnfFailed.SafeToBind("OL26320A") {
		t.Error("SafeToBind(OL id) = true after a BnF primary failure, want false")
	}
}
