package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// countingEnricher counts its calls atomically: the add and refresh syncs ask
// it from their own goroutine.
type countingEnricher struct{ n atomic.Int32 }

func (e *countingEnricher) calls() int { return int(e.n.Load()) }

func (e *countingEnricher) Name() string { return "counting" }
func (e *countingEnricher) EnrichAuthor(context.Context, *models.Author) (*metadata.AuthorProfile, error) {
	e.n.Add(1)
	return &metadata.AuthorProfile{ImageURL: "https://commons.example/k.jpg", ImageSource: &models.ProfileSource{Provider: "wikimedia-commons"}}, nil
}

// Both relink paths merge the photo and bio through MergeProfileField
// (#3287): a bio the new primary supplies replaces an enriched one and drops
// its credit, rather than keep a credit that no longer describes it, while
// an enriched value the primary has nothing for stays with its credit. A
// relink never asks the enrichers: enrichment runs only on the refreshes.
func TestRelinkKeepsProfileCreditsAccurate(t *testing.T) {
	const name = "Kari Nordmann"
	wiki := &models.ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/k", License: "CC BY-SA 4.0"}
	commons := &models.ProfileSource{Provider: "wikimedia-commons", URL: "https://commons.example/File:K.jpg", License: "CC BY 2.5"}
	newFixture := func(t *testing.T) (*relinkUpstreamFixture, *countingEnricher) {
		primary := &searchableAuthorProvider{
			stubMetaProvider:     stubMetaProvider{name: "openlibrary"},
			searchAuthorsByQuery: map[string][]models.Author{name: {{Name: name, ForeignID: "OL1A"}}},
			authors:              map[string]*models.Author{"OL1A": {Name: name, ForeignID: "OL1A", Description: "From the provider."}},
		}
		f := newRelinkUpstreamFixture(t, primary)
		e := &countingEnricher{}
		f.handler.meta.SetAuthorEnrichers(e)
		return f, e
	}
	enrichedAuthor := func(foreignID, provider string) *models.Author {
		return &models.Author{Name: name, ForeignID: foreignID, MetadataProvider: provider,
			Description: "Kari Nordmann is a writer.", DescriptionSource: wiki,
			ImageURL: "https://commons.example/old.jpg", ImageSource: commons}
	}
	check := func(t *testing.T, f *relinkUpstreamFixture, e *countingEnricher, id int64) {
		t.Helper()
		got, err := f.authors.GetByID(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Description != "From the provider." || got.DescriptionSource != nil {
			t.Errorf("bio %q from %+v: the primary's bio should replace the enriched one and drop its credit", got.Description, got.DescriptionSource)
		}
		if got.ImageURL != "https://commons.example/old.jpg" || got.ImageSource == nil || *got.ImageSource != *commons {
			t.Errorf("photo %q from %+v: the enriched photo should stay with its credit", got.ImageURL, got.ImageSource)
		}
		if e.calls() != 0 {
			t.Errorf("a relink asked the enrichers %d times", e.calls())
		}
	}

	t.Run("relink upstream", func(t *testing.T) {
		f, e := newFixture(t)
		author := f.createAuthor(t, enrichedAuthor("abs:kari", ""))
		if rec := f.relink(t, author.ID); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		check(t, f, e, author.ID)
	})
	t.Run("calibre relink", func(t *testing.T) {
		f, e := newFixture(t)
		author := f.createAuthor(t, enrichedAuthor("calibre:author:1", "calibre"))
		if err := f.handler.relinkCalibreAuthor(f.ctx, author); err != nil {
			t.Fatal(err)
		}
		check(t, f, e, author.ID)
	})
}
