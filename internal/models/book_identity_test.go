package models

import "testing"

// TestBookProviderFromForeignID covers the classification the book_identifiers
// table and migration 078's backfill both rely on (#1705). An unprefixed id is
// OpenLibrary, matching the long-standing books.foreign_id convention.
func TestBookProviderFromForeignID(t *testing.T) {
	cases := map[string]string{
		"hc:volume-1":      "hardcover",
		"HC:VOLUME-1":      "hardcover",
		"OL1W":             "openlibrary",
		"calibre:12":       "calibre",
		"abs:lib:item":     "audiobookshelf",
		"gb:xyz":           "googlebooks",
		"dnb:123":          "dnb",
		"":                 "openlibrary",
		"  hc:spaced  ":    "hardcover",
		"something-random": "openlibrary",
	}
	for id, want := range cases {
		if got := BookProviderFromForeignID(id); got != want {
			t.Errorf("BookProviderFromForeignID(%q) = %q, want %q", id, got, want)
		}
	}
}
