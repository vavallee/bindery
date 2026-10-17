package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type refreshEnricher struct {
	calls int
	err   error
}

func (e *refreshEnricher) Name() string { return "refresh-enricher" }
func (e *refreshEnricher) EnrichAuthor(context.Context, *models.Author) (*metadata.AuthorProfile, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	return &metadata.AuthorProfile{ImageURL: "https://commons.example/k.jpg", ImageSource: &models.ProfileSource{Provider: "wikimedia-commons"}}, nil
}

// The staggered refresh asks the enrichers only when the author's enrichment
// stamp is missing or a month old, and stamps only a full answer that was
// saved (#3287). The stamp lives on the row, so a restart does not reset it.
func TestRefreshMetadata_EnrichesOnlyWhenTheStampHasAged(t *testing.T) {
	f := newRefreshFixture(t, 1)
	e := &refreshEnricher{}
	f.s.meta.SetAuthorEnrichers(e)
	ctx := context.Background()
	id := f.ids["OL000A"]
	last := func() *time.Time {
		t.Helper()
		ts, err := f.authors.LastEnrichmentAt(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}

	f.s.refreshMetadata()
	if e.calls != 1 || last() == nil {
		t.Fatalf("first refresh: %d enricher calls, stamp %v; want 1 and a stamp", e.calls, last())
	}
	if a, _ := f.authors.GetByID(ctx, id); a.ImageURL != "https://commons.example/k.jpg" || a.ImageSource == nil {
		t.Errorf("the enriched photo was not saved: %q from %+v", a.ImageURL, a.ImageSource)
	}

	// Due for a profile refresh again, but enriched a day ago: not asked.
	f.stampAgo("OL000A", 25*time.Hour)
	f.s.refreshMetadata()
	if e.calls != 1 {
		t.Errorf("a fresh enrichment stamp was asked again: %d calls", e.calls)
	}

	// A month on, it is asked again.
	if err := f.authors.StampEnrichment(ctx, id, time.Now().Add(-authorEnrichmentInterval-time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.stampAgo("OL000A", 25*time.Hour)
	f.s.refreshMetadata()
	if e.calls != 2 {
		t.Errorf("an aged enrichment stamp was not asked again: %d calls", e.calls)
	}
}

// A failed enrichment is not an answer: nothing is stamped, so the next due
// refresh tries again.
func TestRefreshMetadata_FailedEnrichmentIsNotStamped(t *testing.T) {
	f := newRefreshFixture(t, 1)
	f.s.meta.SetAuthorEnrichers(&refreshEnricher{err: errors.New("wikimedia down")})
	f.s.refreshMetadata()
	if ts, err := f.authors.LastEnrichmentAt(context.Background(), f.ids["OL000A"]); err != nil || ts != nil {
		t.Errorf("a failed enrichment was stamped: %v, %v", ts, err)
	}
	if f.stamped("OL000A") == nil {
		t.Error("the profile refresh itself should still be stamped")
	}
}

// A credited bio (#3287) survives a scheduled refresh whose provider has no
// bio, credit and all; a bio the provider does supply replaces it and takes
// the credit away with it.
func TestPersistRefreshedAuthorKeepsACreditedBio(t *testing.T) {
	f := newRefreshFixture(t, 1)
	ctx := context.Background()
	id := f.ids["OL000A"]
	wiki := &models.ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/k", License: "CC BY-SA 4.0"}
	stored, err := f.authors.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	stored.Description, stored.DescriptionSource = "Kari Nordmann is a writer.", wiki
	if err := f.authors.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	reload := func() *models.Author {
		t.Helper()
		a, err := f.authors.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	author := reload()
	if !f.s.persistRefreshedAuthor(ctx, author, &models.Author{ForeignID: "OL000A"}, author.UpdatedAtRaw) {
		t.Fatal("refresh not written")
	}
	if got := reload(); got.Description != "Kari Nordmann is a writer." || got.DescriptionSource == nil || *got.DescriptionSource != *wiki {
		t.Errorf("after a refresh with no bio: %q from %+v; want the credited bio kept", got.Description, got.DescriptionSource)
	}

	author = reload()
	if !f.s.persistRefreshedAuthor(ctx, author, &models.Author{ForeignID: "OL000A", Description: "From the provider."}, author.UpdatedAtRaw) {
		t.Fatal("refresh not written")
	}
	if got := reload(); got.Description != "From the provider." || got.DescriptionSource != nil {
		t.Errorf("after a refresh with a provider bio: %q from %+v; want the provider's, uncredited", got.Description, got.DescriptionSource)
	}
}
