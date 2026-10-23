package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestListMetadataRefreshDue_FiltersAndOrder pins who the staggered profile
// refresh visits (#2595): monitored authors only, never Calibre shells, never
// refreshed authors first, then the oldest refresh, nobody refreshed more
// recently than the cutoff, and the limit applies after that ordering.
func TestListMetadataRefreshDue_FiltersAndOrder(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour)

	neverA := newDiscoveryTestAuthor(t, repo, "OL1A", nil)
	old := newDiscoveryTestAuthor(t, repo, "OL2A", nil)
	older := newDiscoveryTestAuthor(t, repo, "OL3A", nil)
	recent := newDiscoveryTestAuthor(t, repo, "OL4A", nil)
	neverB := newDiscoveryTestAuthor(t, repo, "OL5A", nil)
	newDiscoveryTestAuthor(t, repo, "OL6A", func(a *models.Author) { a.Monitored = false })
	newDiscoveryTestAuthor(t, repo, "calibre:author:9", nil)
	// An author the user told Bindery to add no new items for still has a
	// profile worth keeping fresh: profile refresh is not discovery.
	noNew := newDiscoveryTestAuthor(t, repo, "OL7A", func(a *models.Author) { a.MonitorNewItems = models.AuthorMonitorNewItemsNone })

	for id, when := range map[int64]time.Time{
		old.ID:    now.Add(-30 * time.Hour),
		older.ID:  now.Add(-90 * time.Hour),
		recent.ID: now.Add(-2 * time.Hour),
		noNew.ID:  now.Add(-48 * time.Hour),
	} {
		if err := repo.StampMetadataRefresh(ctx, id, when); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.ListMetadataRefreshDue(ctx, now, cutoff, 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	want := []int64{neverA.ID, neverB.ID, older.ID, noNew.ID, old.ID}
	if len(ids) != len(want) {
		t.Fatalf("due = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("due = %v, want %v", ids, want)
		}
	}

	limited, err := repo.ListMetadataRefreshDue(ctx, now, cutoff, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 3 || limited[0].ID != neverA.ID || limited[2].ID != older.ID {
		t.Fatalf("limited = %v, want the three stalest", limited)
	}

	n, err := repo.CountMetadataRefreshEligible(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Errorf("eligible = %d, want 6 (monitored, not calibre)", n)
	}
}

// TestStampMetadataRefresh_RoundTripsAndKeepsOtherFields checks the stamp is a
// single column write: an edit saved while a refresh was running survives it.
func TestStampMetadataRefresh_RoundTripsAndKeepsOtherFields(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)

	a := newDiscoveryTestAuthor(t, repo, "OL1A", func(a *models.Author) { a.Description = "kept" })
	when := time.Date(2026, 10, 14, 9, 30, 15, 0, time.UTC)
	if err := repo.StampMetadataRefresh(ctx, a.ID, when); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastMetadataRefreshAt == nil || !got.LastMetadataRefreshAt.Equal(when) {
		t.Errorf("LastMetadataRefreshAt = %v, want %v", got.LastMetadataRefreshAt, when)
	}
	if got.Description != "kept" {
		t.Errorf("Description = %q, want it untouched", got.Description)
	}
}
