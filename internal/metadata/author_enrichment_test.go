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

// enriched looks the author up and enriches the copy, the way the profile
// refreshes do.
func enriched(t *testing.T, a *Aggregator) (*models.Author, bool) {
	t.Helper()
	got, err := a.GetAuthor(context.Background(), "nb:author:1")
	if err != nil {
		t.Fatal(err)
	}
	return got, a.EnrichAuthor(context.Background(), got)
}

func TestEnrichAuthorFillsEmptyPhotoAndBio(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1", Name: "Kari Nordmann"})
	pics, text := &stubAuthorEnricher{name: "pics", profile: photo("https://img.example/k.jpg")}, &stubAuthorEnricher{name: "text", profile: bio("Kari Nordmann is a writer.")}
	a.SetAuthorEnrichers(pics, text)

	got, answered := enriched(t, a)
	if !answered {
		t.Error("two enrichers that answered: answered = false")
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

// GetAuthor never enriches: an import or an add that looks up many authors
// sends the enrichers nothing, and the cache holds the primary's record alone.
func TestGetAuthorDoesNotEnrich(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1"})
	e := &stubAuthorEnricher{name: "e", profile: photo("https://img.example/k.jpg")}
	a.SetAuthorEnrichers(e)
	got, err := a.GetAuthor(context.Background(), "nb:author:1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageURL != "" || e.calls != 0 {
		t.Errorf("GetAuthor enriched: photo %q, %d enricher calls", got.ImageURL, e.calls)
	}
	ctx := WithCacheBypass(context.Background())
	if got, _ := a.GetAuthor(ctx, "nb:author:1"); got.ImageURL != "" || e.calls != 0 {
		t.Errorf("a cache bypass lookup enriched: photo %q, %d calls", got.ImageURL, e.calls)
	}
}

func TestEnrichAuthorNeverReplacesThePrimarysOwnValues(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1", ImageURL: "https://primary.example/k.jpg", Description: "From the primary."})
	e := &stubAuthorEnricher{name: "e", profile: &AuthorProfile{ImageURL: "https://other.example/k.jpg", Description: "Other."}}
	a.SetAuthorEnrichers(e)

	got, answered := enriched(t, a)
	if got.ImageURL != "https://primary.example/k.jpg" || got.Description != "From the primary." || got.ImageSource != nil || got.DescriptionSource != nil {
		t.Errorf("primary values replaced: %+v", got)
	}
	if e.calls != 0 || !answered {
		t.Errorf("an author with a photo and a bio asked the enricher %d times (want none), answered=%v", e.calls, answered)
	}
}

func TestEnrichAuthorAsksInOrderAndSkipsFailures(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1"})
	broken := &stubAuthorEnricher{name: "broken", err: errors.New("down")}
	first := &stubAuthorEnricher{name: "first", profile: photo("https://first.example/k.jpg")}
	second := &stubAuthorEnricher{name: "second", profile: &AuthorProfile{ImageURL: "https://second.example/k.jpg", Description: "Second."}}
	a.SetAuthorEnrichers(broken, first, second)

	got, answered := enriched(t, a)
	if got.ImageURL != "https://first.example/k.jpg" {
		t.Errorf("photo %q; want the first enricher's", got.ImageURL)
	}
	if got.Description != "Second." {
		t.Errorf("bio %q; a later enricher should fill what an earlier one left empty", got.Description)
	}
	if answered {
		t.Error("a failed enricher must not count as an answer, or the refresh would stamp it")
	}
}

// A partial answer fills what it has but is not an answer to stamp.
func TestEnrichAuthorPartialIsNotAnswered(t *testing.T) {
	a := nbAggregator(&models.Author{ForeignID: "nb:author:1"})
	p := photo("https://img.example/k.jpg")
	p.Partial = true
	a.SetAuthorEnrichers(&stubAuthorEnricher{name: "e", profile: p})
	got, answered := enriched(t, a)
	if got.ImageURL == "" || answered {
		t.Errorf("photo %q, answered=%v; want the photo and no answer", got.ImageURL, answered)
	}
}

func TestAuthorEnricherGuards(t *testing.T) {
	var nilAgg *Aggregator
	nilAgg.SetAuthorEnrichers(&stubAuthorEnricher{name: "e"})
	if names := nilAgg.AuthorEnricherNames(); names != nil {
		t.Errorf("nil aggregator names = %v", names)
	}

	a := nbAggregator(&models.Author{ForeignID: "nb:author:1"})
	if _, answered := enriched(t, a); answered {
		t.Error("no enrichers is not an answer: stamping it would hold off enrichment for 30 days once one is switched on")
	}
	empty := &stubAuthorEnricher{name: "empty"}
	a.SetAuthorEnrichers(nil, empty, &stubAuthorEnricher{name: "pics", profile: photo("https://img.example/k.jpg")})
	if names := a.AuthorEnricherNames(); !slices.Equal(names, []string{"empty", "pics"}) {
		t.Errorf("a nil enricher was kept: %v", names)
	}
	got, answered := enriched(t, a)
	if empty.calls != 1 || got.ImageURL != "https://img.example/k.jpg" || !answered {
		t.Errorf("an enricher with nothing should be passed over: calls %d, photo %q, answered %v", empty.calls, got.ImageURL, answered)
	}
	if a.EnrichAuthor(context.Background(), nil) {
		t.Error("a nil author was answered")
	}

	var core *aggregatorCore
	core.EnrichAuthor(context.Background(), &models.Author{})
}
