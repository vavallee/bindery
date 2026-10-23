package db

import (
	"context"
	"testing"
	"time"
)

// The enrichment stamp (#3287) is written by a single column UPDATE, so a
// whole row Update from an older snapshot leaves it alone, and it reads back
// nil for an author never enriched or not there at all.
func TestStampEnrichment(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)

	a := newDiscoveryTestAuthor(t, repo, "OL10A", nil)
	if got, err := repo.LastEnrichmentAt(ctx, a.ID); err != nil || got != nil {
		t.Fatalf("never enriched: %v, %v", got, err)
	}
	snapshot := *a
	when := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	if err := repo.StampEnrichment(ctx, a.ID, when); err != nil {
		t.Fatal(err)
	}
	snapshot.Description = "edited by the user"
	if err := repo.Update(ctx, &snapshot); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.LastEnrichmentAt(ctx, a.ID); err != nil || got == nil || !got.Equal(when) {
		t.Fatalf("LastEnrichmentAt = %v, %v; want %v", got, err, when)
	}
	if got, err := repo.LastEnrichmentAt(ctx, 999999); err != nil || got != nil {
		t.Errorf("missing author: %v, %v", got, err)
	}

	database.Close()
	if err := repo.StampEnrichment(ctx, a.ID, when); err == nil {
		t.Error("stamping on a closed database gave no error")
	}
	if _, err := repo.LastEnrichmentAt(ctx, a.ID); err == nil {
		t.Error("reading on a closed database gave no error")
	}
}

// An author is due for enrichment when it was never enriched, when its stamp
// is older than AuthorEnrichmentInterval, and when the stamp cannot be read.
func TestEnrichmentDue(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := NewAuthorRepo(database)
	a := newDiscoveryTestAuthor(t, repo, "OL11A", nil)

	if !repo.EnrichmentDue(ctx, a.ID) {
		t.Error("never enriched: want due")
	}
	if err := repo.StampEnrichment(ctx, a.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if repo.EnrichmentDue(ctx, a.ID) {
		t.Error("enriched an hour ago: want not due")
	}
	if err := repo.StampEnrichment(ctx, a.ID, time.Now().Add(-AuthorEnrichmentInterval-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !repo.EnrichmentDue(ctx, a.ID) {
		t.Error("stamp past the interval: want due")
	}
	_ = database.Close()
	if !repo.EnrichmentDue(ctx, a.ID) {
		t.Error("unreadable stamp: want due")
	}
}
