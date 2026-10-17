package metadata

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

type stubAuthorEnricher struct {
	name    string
	profile *AuthorProfile
	err     error
	calls   int
}

func (s *stubAuthorEnricher) Name() string { return s.name }
func (s *stubAuthorEnricher) EnrichAuthor(context.Context, *models.Author) (*AuthorProfile, error) {
	s.calls++
	return s.profile, s.err
}

func photo(url string) *AuthorProfile {
	return &AuthorProfile{ImageURL: url, ImageSource: &models.ProfileSource{Provider: "wikimedia-commons", URL: "https://commons.example/f"}}
}

func bio(text string) *AuthorProfile {
	return &AuthorProfile{Description: text, DescriptionSource: &models.ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/k"}}
}

func nbAggregator(author *models.Author) *Aggregator {
	return NewAggregator(&mockProvider{name: "nb", getAuthor: author})
}

func TestGetAuthorFillsEmptyPhotoAndBioFromEnrichers(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1", Name: "Kari Nordmann"})
	pics, text := &stubAuthorEnricher{name: "pics", profile: photo("https://img.example/k.jpg")}, &stubAuthorEnricher{name: "text", profile: bio("Kari Nordmann is a writer.")}
	a.SetAuthorEnrichers(pics, text)

	got, err := a.GetAuthor(context.Background(), "nb:author:1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageURL != "https://img.example/k.jpg" || got.ImageSource == nil || got.ImageSource.Provider != "wikimedia-commons" {
		t.Errorf("photo %q from %+v", got.ImageURL, got.ImageSource)
	}
	if got.Description != "Kari Nordmann is a writer." || got.DescriptionSource == nil || got.DescriptionSource.Provider != "wikipedia" {
		t.Errorf("bio %q from %+v", got.Description, got.DescriptionSource)
	}
	if names := a.AuthorEnricherNames(); !slices.Equal(names, []string{"pics", "text"}) {
		t.Errorf("enricher names %v", names)
	}
}

func TestGetAuthorNeverReplacesThePrimarysOwnValues(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1", ImageURL: "https://primary.example/k.jpg", Description: "From the primary."})
	e := &stubAuthorEnricher{name: "e", profile: &AuthorProfile{ImageURL: "https://other.example/k.jpg", Description: "Other."}}
	a.SetAuthorEnrichers(e)

	got, _ := a.GetAuthor(context.Background(), "nb:author:1")
	if got.ImageURL != "https://primary.example/k.jpg" || got.Description != "From the primary." || got.ImageSource != nil || got.DescriptionSource != nil {
		t.Errorf("primary values replaced: %+v", got)
	}
	if e.calls != 0 {
		t.Errorf("an author with a photo and a bio asked the enricher %d times; want none", e.calls)
	}
}

func TestGetAuthorAsksEnrichersInOrderAndSkipsFailures(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1"})
	broken := &stubAuthorEnricher{name: "broken", err: errors.New("down")}
	first := &stubAuthorEnricher{name: "first", profile: photo("https://first.example/k.jpg")}
	second := &stubAuthorEnricher{name: "second", profile: &AuthorProfile{ImageURL: "https://second.example/k.jpg", Description: "Second."}}
	a.SetAuthorEnrichers(broken, first, second)

	got, err := a.GetAuthor(context.Background(), "nb:author:1")
	if err != nil {
		t.Fatalf("an enricher failure failed the lookup: %v", err)
	}
	if got.ImageURL != "https://first.example/k.jpg" {
		t.Errorf("photo %q; want the first enricher's", got.ImageURL)
	}
	if got.Description != "Second." {
		t.Errorf("bio %q; a later enricher should fill what an earlier one left empty", got.Description)
	}
}

// The cache keeps the primary's record alone, so switching enrichment off
// takes effect on the next lookup rather than when the cache entry expires.
func TestGetAuthorKeepsEnrichmentOutOfTheCache(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1"})
	a.SetAuthorEnrichers(&stubAuthorEnricher{name: "e", profile: photo("https://img.example/k.jpg")})
	if got, _ := a.GetAuthor(context.Background(), "nb:author:1"); got.ImageURL == "" {
		t.Fatal("not enriched")
	}

	a.SetAuthorEnrichers()
	got, _ := a.GetAuthor(context.Background(), "nb:author:1")
	if got.ImageURL != "" || got.ImageSource != nil {
		t.Errorf("enriched photo %q survived in the cache after the enricher was removed", got.ImageURL)
	}
}

func TestAuthorEnricherGuards(t *testing.T) {
	var nilAgg *Aggregator
	nilAgg.SetAuthorEnrichers(&stubAuthorEnricher{name: "e"})
	if names := nilAgg.AuthorEnricherNames(); names != nil {
		t.Errorf("nil aggregator names = %v", names)
	}

	a := nbAggregator(&models.Author{ForeignID: "nb:author:1"})
	empty := &stubAuthorEnricher{name: "empty"}
	a.SetAuthorEnrichers(nil, empty, &stubAuthorEnricher{name: "pics", profile: photo("https://img.example/k.jpg")})
	if names := a.AuthorEnricherNames(); !slices.Equal(names, []string{"empty", "pics"}) {
		t.Errorf("a nil enricher was kept: %v", names)
	}
	got, _ := a.GetAuthor(context.Background(), "nb:author:1")
	if empty.calls != 1 || got.ImageURL != "https://img.example/k.jpg" {
		t.Errorf("an enricher with nothing should be passed over: calls %d, photo %q", empty.calls, got.ImageURL)
	}

	var core *aggregatorCore
	core.enrichAuthor(context.Background(), nil)
}
