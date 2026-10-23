package main

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// enricherSetCases cover #1890. The cases that name no disabled enricher must
// match what the same primary served before the setting existed.
var enricherSetCases = []struct {
	name     string
	primary  string
	disabled string
	gbKey    string
	want     []string
}{
	{"empty list is today's set", "", "", "", []string{"openlibrary", "hardcover", "dnb"}},
	{"dnb off", "", "dnb", "", []string{"openlibrary", "hardcover"}},
	{"hardcover off", "", "hardcover", "", []string{"openlibrary", "dnb"}},
	{"openlibrary off while hardcover and dnb stay", "dnb", "openlibrary", "", []string{"dnb", "hardcover"}},
	{"googlebooks off when keyed", "", "googlebooks", "k", []string{"openlibrary", "hardcover", "dnb"}},
	{"googlebooks on when keyed and not listed", "", "dnb", "k", []string{"openlibrary", "googlebooks", "hardcover"}},
	{"all off leaves only the primary", "", "openlibrary,hardcover,googlebooks,dnb", "k", []string{"openlibrary"}},
	{"primary named in the list is ignored", "dnb", "dnb", "", []string{"dnb", "hardcover", "openlibrary"}},
	{"openlibrary primary named in the list is ignored", "", "openlibrary,dnb", "", []string{"openlibrary", "hardcover"}},
	{"unknown name is ignored", "", "goodreads,dnb", "", []string{"openlibrary", "hardcover"}},
	{"nb is not an enricher, naming it changes nothing", "nb", "nb", "", []string{"nb", "hardcover", "openlibrary", "dnb"}},
	{"case whitespace and duplicates", "", " DNB , dnb,,Hardcover ", "", []string{"openlibrary"}},
}

func TestMetadataEnrichersDisabledSelectsTheSet(t *testing.T) {
	for _, tc := range enricherSetCases {
		t.Run(tc.name, func(t *testing.T) {
			m, repo := metadataProvidersFixture(t)
			storeProviderConfig(t, repo, providerConfig{primary: tc.primary, googleBooksKey: tc.gbKey})
			if tc.disabled != "" {
				// Straight to the repo: the unknown and unnormalised forms
				// can only arrive out of band, and build must cope.
				if err := repo.Set(context.Background(), api.SettingMetadataEnrichersDisabled, tc.disabled); err != nil {
					t.Fatal(err)
				}
			}
			set := m.build(context.Background())
			got := metadata.NewAggregator(set.primary, set.enrichers...).ProviderNames()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("set = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMetadataEnrichersDisabledAppliesLive saves the setting on a running
// aggregator through the settings hook and checks the set changes without a
// restart, then comes back when the setting is cleared.
func TestMetadataEnrichersDisabledAppliesLive(t *testing.T) {
	m, repo := metadataProvidersFixture(t)
	ctx := context.Background()
	set := m.build(ctx)
	agg := metadata.NewAggregator(set.primary, set.enrichers...)
	hook := metadataProvidersReconfigurer(m, agg)

	if err := repo.Set(ctx, api.SettingMetadataEnrichersDisabled, "dnb"); err != nil {
		t.Fatal(err)
	}
	if handled, err := hook(ctx, api.SettingMetadataEnrichersDisabled); !handled || err != nil {
		t.Fatalf("hook handled=%v err=%v", handled, err)
	}
	if got, want := agg.ProviderNames(), []string{"openlibrary", "hardcover"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after disabling dnb: %v, want %v", got, want)
	}
	if err := repo.Delete(ctx, api.SettingMetadataEnrichersDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := hook(ctx, api.SettingMetadataEnrichersDisabled); err != nil {
		t.Fatal(err)
	}
	if got, want := agg.ProviderNames(), []string{"openlibrary", "hardcover", "dnb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after clearing: %v, want %v", got, want)
	}
}

// TestAudiobookSourcesFollowTheSetting checks audible and audnex are read from
// the same list, leave the enricher set alone, and survive the live apply.
func TestAudiobookSourcesFollowTheSetting(t *testing.T) {
	m, repo := metadataProvidersFixture(t)
	ctx := context.Background()
	set := m.build(ctx)
	if set.audibleOff || set.audnexOff {
		t.Fatal("a default build disabled an audiobook source")
	}
	if err := repo.Set(ctx, api.SettingMetadataEnrichersDisabled, "Audible, audnex"); err != nil {
		t.Fatal(err)
	}
	set = m.build(ctx)
	if !set.audibleOff || !set.audnexOff {
		t.Errorf("audibleOff=%v audnexOff=%v, want both", set.audibleOff, set.audnexOff)
	}
	if got := metadata.NewAggregator(set.primary, set.enrichers...).ProviderNames(); !reflect.DeepEqual(got, []string{"openlibrary", "hardcover", "dnb"}) {
		t.Errorf("enrichers changed: %v", got)
	}
	// apply must hand the flags to the running aggregator: counting clients
	// see nothing while the setting names them, and calls again once cleared.
	aud, ax := &countingAudible{}, &countingAudnex{}
	agg := metadata.NewAggregator(set.primary, set.enrichers...).WithAudibleCatalogue(aud).WithAudnexClient(ax)
	if err := m.apply(ctx, agg); err != nil {
		t.Fatal(err)
	}
	_, _ = agg.GetAuthorAudiobooks(ctx, "Author A")
	_, _ = agg.GetCanonicalBookByASIN(ctx, "B01N5IB20Q")
	if aud.calls.Load() != 0 || ax.calls.Load() != 0 {
		t.Fatalf("disabled sources were called: audible=%d audnex=%d", aud.calls.Load(), ax.calls.Load())
	}
	if err := repo.Delete(ctx, api.SettingMetadataEnrichersDisabled); err != nil {
		t.Fatal(err)
	}
	if err := m.apply(ctx, agg); err != nil {
		t.Fatal(err)
	}
	_, _ = agg.GetAuthorAudiobooks(ctx, "Author A")
	_, _ = agg.GetCanonicalBookByASIN(ctx, "B01N5IB20Q")
	if aud.calls.Load() == 0 || ax.calls.Load() == 0 {
		t.Errorf("re-enabled sources were not called: audible=%d audnex=%d", aud.calls.Load(), ax.calls.Load())
	}
}

type countingAudible struct{ calls atomic.Int32 }

func (c *countingAudible) SearchBooksByAuthor(context.Context, string) ([]models.Book, error) {
	c.calls.Add(1)
	return nil, nil
}

type countingAudnex struct{ calls atomic.Int32 }

func (c *countingAudnex) GetBook(context.Context, string) (*audnex.Book, error) {
	c.calls.Add(1)
	return nil, nil
}

// callCounter is a provider that counts every call it receives.
type callCounter struct {
	metadata.Provider
	name  string
	calls *int32
}

func (c *callCounter) hit()         { atomic.AddInt32(c.calls, 1) }
func (c *callCounter) Name() string { return c.name }
func (c *callCounter) SearchAuthors(context.Context, string) ([]models.Author, error) {
	c.hit()
	return nil, nil
}
func (c *callCounter) SearchBooks(context.Context, string) ([]models.Book, error) {
	c.hit()
	return nil, nil
}
func (c *callCounter) GetBookByISBN(context.Context, string) (*models.Book, error) {
	c.hit()
	return nil, nil
}
func (c *callCounter) GetBook(context.Context, string) (*models.Book, error) {
	c.hit()
	return nil, nil
}

// TestDisabledEnricherReceivesNoCalls drives an aggregator built from build
// and checks the disabled provider is never asked, while an enabled one is.
func TestDisabledEnricherReceivesNoCalls(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSettingsRepo(database)
	var olN, dnbN, hcN int32
	m := newMetadataProviders(repo,
		&callCounter{name: "openlibrary", calls: &olN},
		&callCounter{name: "dnb", calls: &dnbN},
		&callCounter{name: "hardcover", calls: &hcN})
	ctx := context.Background()
	if err := repo.Set(ctx, api.SettingMetadataEnrichersDisabled, "dnb"); err != nil {
		t.Fatal(err)
	}
	set := m.build(ctx)
	agg := metadata.NewAggregator(set.primary, set.enrichers...)

	_, _ = agg.SearchBooks(ctx, "dune")
	_, _ = agg.SearchAuthors(ctx, "herbert")
	_, _ = agg.GetBookByISBN(ctx, "9780441013593")

	if n := atomic.LoadInt32(&dnbN); n != 0 {
		t.Errorf("disabled dnb received %d calls", n)
	}
	if atomic.LoadInt32(&hcN) == 0 || atomic.LoadInt32(&olN) == 0 {
		t.Errorf("enabled providers were not asked: hardcover=%d openlibrary=%d", hcN, olN)
	}
}
