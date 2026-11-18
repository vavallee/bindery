package audiblesync

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func intPtr(n int) *int           { return &n }
func floatPtr(f float64) *float64 { return &f }

func sampleBook() CatalogBook {
	return CatalogBook{
		ASIN:      "b00abcdefg",
		Title:     "Ancillary Justice",
		Subtitle:  "Imperial Radch, Book 1",
		Authors:   []CatalogAuthor{{Name: " "}, {Name: "Ann Leckie", ASIN: "B001"}},
		Narrators: []string{"Adjoa Andoh", " "},
		Series: []SeriesEntry{
			{Title: "Imperial Radch", Sequence: "1", SeriesASIN: "B00SERIES1"},
			{Title: "No ASIN", Sequence: "2"},
		},
		PrimarySeries: &SeriesEntry{Title: "Imperial Radch", Sequence: "1", SeriesASIN: "B00SERIES1"},
		Genres:        []string{"Science Fiction"},
		LengthSeconds: intPtr(73800),
		ReleaseDate:   "2014-01-07",
		Language:      "en",
		Publisher:     "Hachette Audio",
		CoverURL:      "https://m.media-amazon.com/images/I/x._SL1215_.jpg",
		Rating:        floatPtr(4.3),
		RatingCount:   1200,
	}
}

func TestToBookMapsAnAudiobook(t *testing.T) {
	b := ToBook(sampleBook())
	if b == nil {
		t.Fatal("nil book")
	}
	if b.ForeignID != "audible:B00ABCDEFG" || b.ASIN != "B00ABCDEFG" {
		t.Errorf("ids = %q %q", b.ForeignID, b.ASIN)
	}
	if b.Title != "Ancillary Justice: Imperial Radch, Book 1" {
		t.Errorf("title = %q", b.Title)
	}
	if b.MediaType != models.MediaTypeAudiobook || b.MetadataProvider != "audible" {
		t.Errorf("media/provider = %q %q", b.MediaType, b.MetadataProvider)
	}
	if b.Narrator != "Adjoa Andoh" || b.DurationSeconds != 73800 {
		t.Errorf("narrator/duration = %q %d", b.Narrator, b.DurationSeconds)
	}
	if b.Author == nil || b.Author.Name != "Ann Leckie" {
		t.Errorf("author = %+v", b.Author)
	}
	if b.ReleaseDate == nil || b.ReleaseDate.Year() != 2014 {
		t.Errorf("release = %v", b.ReleaseDate)
	}
	if b.AverageRating != 4.3 || b.RatingsCount != 1200 || b.Language != "eng" {
		t.Errorf("rating/lang = %v %d %q", b.AverageRating, b.RatingsCount, b.Language)
	}
	if len(b.SeriesRefs) != 1 || b.SeriesRefs[0].ForeignID != "audible-series:B00SERIES1" ||
		b.SeriesRefs[0].Position != "1" || !b.SeriesRefs[0].Primary {
		t.Errorf("series refs = %+v", b.SeriesRefs)
	}
}

func TestToBookSkipsSubtitleAlreadyInTitle(t *testing.T) {
	cb := CatalogBook{ASIN: "B00ABCDEFG", Title: "Dune: Book One", Subtitle: "Book One"}
	if b := ToBook(cb); b.Title != "Dune: Book One" {
		t.Errorf("title = %q", b.Title)
	}
}

func TestToBookRejectsIncomplete(t *testing.T) {
	if ToBook(CatalogBook{Title: "No ASIN"}) != nil || ToBook(CatalogBook{ASIN: "B00ABCDEFG"}) != nil {
		t.Fatal("an entry with no ASIN or no title must map to nil")
	}
}
