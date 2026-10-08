package openlibrary

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func coverLangEntry(key, language string, cover int) editionEntry {
	e := editionEntry{Key: "/books/" + key}
	if language != "" {
		e.Languages = []struct {
			Key string `json:"key"`
		}{{Key: "/languages/" + language}}
	}
	if cover > 0 {
		e.Covers = []int{cover}
	}
	return e
}

// The work's cover_edition, the edition OpenLibrary's own page features, is
// the canonical edition for the derived language and cover (#1779). The
// editions endpoint returns no meaningful order, so its first few entries can
// all be translations: here they are French, and the work used to be read as
// French and given a French cover.
func TestSampleWorkEditions_PrefersCoverEdition(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL45804W.json": `{"key":"/works/OL45804W","title":"Fantastic Mr Fox","cover_edition":{"key":"/books/OL7M"}}`,
		"/books/OL7M.json":     jsonStr(coverLangEntry("OL7M", "eng", 77)),
		"/works/OL45804W/editions.json": jsonStr(editionsResponse{Entries: []editionEntry{
			coverLangEntry("OL1M", "fre", 11),
			coverLangEntry("OL2M", "fre", 12),
			coverLangEntry("OL3M", "fre", 13),
		}}),
	})
	c.workSampleCache = map[string]workEditionSample{}

	books := []models.Book{{ForeignID: "OL45804W", Title: "Fantastic Mr Fox"}}
	c.FillMissingWorkLanguages(context.Background(), books)
	c.FillMissingWorkCovers(context.Background(), books)

	if books[0].Language != "eng" {
		t.Errorf("Language = %q, want eng from the cover edition", books[0].Language)
	}
	if want := coverURL + "/b/id/77-L.jpg"; books[0].ImageURL != want {
		t.Errorf("ImageURL = %q, want the cover edition's %q", books[0].ImageURL, want)
	}
}

// Without a cover_edition, the sampled cover is taken from an edition in the
// language the sample settled on, not from whichever edition came first.
func TestSampleWorkEditions_CoverFollowsSampledLanguage(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL1W.json": `{"key":"/works/OL1W","title":"Work"}`,
		"/works/OL1W/editions.json": jsonStr(editionsResponse{Entries: []editionEntry{
			coverLangEntry("OL1M", "fre", 11),
			coverLangEntry("OL2M", "eng", 0),
			coverLangEntry("OL3M", "eng", 33),
			coverLangEntry("OL4M", "eng", 0),
		}}),
	})
	c.workSampleCache = map[string]workEditionSample{}

	books := []models.Book{{ForeignID: "OL1W", Title: "Work"}}
	c.FillMissingWorkLanguages(context.Background(), books)
	c.FillMissingWorkCovers(context.Background(), books)

	if books[0].Language != "eng" {
		t.Fatalf("Language = %q, want eng (majority)", books[0].Language)
	}
	if want := coverURL + "/b/id/33-L.jpg"; books[0].ImageURL != want {
		t.Errorf("ImageURL = %q, want the English edition's %q, not the French first entry's", books[0].ImageURL, want)
	}
}
