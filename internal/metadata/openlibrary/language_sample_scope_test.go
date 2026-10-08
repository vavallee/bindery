package openlibrary

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// FillMissingWorkLanguages only samples works OpenLibrary owns (#3091). A
// Hardcover, Audible or DNB supplement has no /works/{id}/editions.json, so
// asking for one is a guaranteed miss: one wasted request per work, per pass.
func TestFillMissingWorkLanguages_SkipsWorksOpenLibraryDoesNotOwn(t *testing.T) {
	rt := &countingTransport{body: editionSampleBody}
	c := newSamplingClient(rt)
	books := []models.Book{
		{ForeignID: "hc:whipping-mek", MetadataProvider: "hardcover"},
		{ForeignID: "hc:the-faces-of-a-martyr"},
		{ForeignID: "audible:B01CZ0WTEM", MetadataProvider: "audible"},
		{ForeignID: "dnb:1305873874"},
		{ForeignID: "OL1W", MetadataProvider: "openlibrary"},
	}

	if filled := c.FillMissingWorkLanguages(context.Background(), books); filled != 1 {
		t.Fatalf("filled = %d, want 1 (the OpenLibrary work only)", filled)
	}
	for _, b := range books[:4] {
		if b.Language != "" {
			t.Errorf("%s got language %q from an OpenLibrary sample it does not have", b.ForeignID, b.Language)
		}
	}
	urls, _ := rt.snapshot()
	for _, u := range urls {
		for _, foreign := range []string{"hc:", "audible:", "dnb:", "hc%3A", "audible%3A", "dnb%3A"} {
			if strings.Contains(u, foreign) {
				t.Errorf("requested a work OpenLibrary does not own: %s", u)
			}
		}
	}
}
