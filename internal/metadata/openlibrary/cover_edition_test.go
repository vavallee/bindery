package openlibrary

import (
	"context"
	"net/http"
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

// The most expensive path, pinned: the featured edition carries a language but
// no cover, so the sample is still needed for the cover. Work record, cover
// edition and editions sample, three requests, each made once and shared by
// both samplers (#1779).
func TestSampleWorkEditions_ThreeRequestPath(t *testing.T) {
	calls := map[string]int{}
	count := func(body string) func(*http.Request) string {
		return func(r *http.Request) string {
			calls[r.URL.Path]++
			return body
		}
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL9W.json":          count(`{"key":"/works/OL9W","title":"Work","cover_edition":{"key":"/books/OL90M"}}`),
		"/books/OL90M.json":         count(jsonStr(coverLangEntry("OL90M", "eng", 0))),
		"/works/OL9W/editions.json": count(jsonStr(editionsResponse{Entries: []editionEntry{coverLangEntry("OL91M", "fre", 91), coverLangEntry("OL92M", "eng", 92)}})),
	})
	c.workSampleCache = map[string]workEditionSample{}

	books := []models.Book{{ForeignID: "OL9W", Title: "Work"}}
	c.FillMissingWorkLanguages(context.Background(), books)
	c.FillMissingWorkCovers(context.Background(), books)

	if books[0].Language != "eng" {
		t.Errorf("Language = %q, want eng from the cover edition", books[0].Language)
	}
	if want := coverURL + "/b/id/92-L.jpg"; books[0].ImageURL != want {
		t.Errorf("ImageURL = %q, want the sampled English edition's %q", books[0].ImageURL, want)
	}
	total := 0
	for path, n := range calls {
		total += n
		if n != 1 {
			t.Errorf("%s requested %d times, want once", path, n)
		}
	}
	if total != 3 {
		t.Errorf("requests = %d (%v), want 3: work record, cover edition, editions sample", total, calls)
	}
}

// GetBook takes the featured edition's cover when the work record carries no
// cover of its own (#1779), costing one request only in that case.
func TestGetBook_UsesCoverEditionWhenWorkHasNoCover(t *testing.T) {
	featured := 0
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL5W.json": `{"key":"/works/OL5W","title":"Work","cover_edition":{"key":"/books/OL50M"}}`,
		"/books/OL50M.json": func(*http.Request) string {
			featured++
			return jsonStr(coverLangEntry("OL50M", "eng", 50))
		},
		"/works/OL6W.json": `{"key":"/works/OL6W","title":"Work","covers":[60],"cover_edition":{"key":"/books/OL60M"}}`,
		"/books/OL60M.json": func(*http.Request) string {
			featured++
			return jsonStr(coverLangEntry("OL60M", "eng", 61))
		},
	})

	book, err := c.GetBook(context.Background(), "OL5W")
	if err != nil {
		t.Fatal(err)
	}
	if want := coverURL + "/b/id/50-L.jpg"; book.ImageURL != want {
		t.Errorf("ImageURL = %q, want the cover edition's %q", book.ImageURL, want)
	}
	book, err = c.GetBook(context.Background(), "OL6W")
	if err != nil {
		t.Fatal(err)
	}
	if want := coverURL + "/b/id/60-L.jpg"; book.ImageURL != want {
		t.Errorf("ImageURL = %q, want the work's own %q", book.ImageURL, want)
	}
	if featured != 1 {
		t.Errorf("cover edition fetched %d times, want once (only for the work with no cover)", featured)
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
