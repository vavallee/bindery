package db

import (
	"context"
	"testing"
	"time"
)

// A stamp in the future (clock moved back, restored backup) must not hide an
// author until the clock catches up: it counts as due, and sorts with the
// never refreshed.
func TestListMetadataRefreshDue_FutureStampIsDue(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	future := newDiscoveryTestAuthor(t, repo, "OL1A", nil)
	old := newDiscoveryTestAuthor(t, repo, "OL2A", nil)
	if err := repo.StampMetadataRefresh(ctx, future.ID, now.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.StampMetadataRefresh(ctx, old.ID, now.Add(-30*time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListMetadataRefreshDue(ctx, now, now.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != future.ID {
		t.Fatalf("due = %v, want the future stamped author first then the old one", got)
	}
}
