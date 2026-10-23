package db

import (
	"context"
	"testing"
	"time"
)

func TestListMetadataRefreshDue_NonPositiveLimitReturnsNothing(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewAuthorRepo(database)
	newDiscoveryTestAuthor(t, repo, "OL1A", nil)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, limit := range []int{0, -3} {
		got, err := repo.ListMetadataRefreshDue(context.Background(), now, now.Add(-time.Hour), limit)
		if err != nil || len(got) != 0 {
			t.Fatalf("limit %d = %v, %v; want nothing", limit, got, err)
		}
	}
}

// The cutoff is strict: an author refreshed exactly at the cutoff is not yet
// due, one a nanosecond older is. A stamp equal to now is a real recent
// refresh, not a future one, so it must not read as never refreshed.
func TestListMetadataRefreshDue_Boundaries(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour)

	atCutoff := newDiscoveryTestAuthor(t, repo, "OL1A", nil)
	justOlder := newDiscoveryTestAuthor(t, repo, "OL2A", nil)
	atNow := newDiscoveryTestAuthor(t, repo, "OL3A", nil)
	for id, when := range map[int64]time.Time{
		atCutoff.ID:  cutoff,
		justOlder.ID: cutoff.Add(-time.Nanosecond),
		atNow.ID:     now,
	} {
		if err := repo.StampMetadataRefresh(ctx, id, when); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.ListMetadataRefreshDue(ctx, now, cutoff, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != justOlder.ID {
		var ids []int64
		for _, a := range got {
			ids = append(ids, a.ID)
		}
		t.Fatalf("due = %v, want only %d (the author one nanosecond past the cutoff)", ids, justOlder.ID)
	}
}

// An unreadable stamp must not exempt an author from refresh forever, and
// stamps written in the older text layouts compare by time, not by string.
func TestListMetadataRefreshDue_UnparseableAndLegacyLayouts(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour)

	garbage := newDiscoveryTestAuthor(t, repo, "OL1A", nil)
	legacyOld := newDiscoveryTestAuthor(t, repo, "OL2A", nil)
	legacyFresh := newDiscoveryTestAuthor(t, repo, "OL3A", nil)
	canonicalOlder := newDiscoveryTestAuthor(t, repo, "OL4A", nil)

	set := func(id int64, raw string) {
		t.Helper()
		if _, err := database.ExecContext(ctx, `UPDATE authors SET last_metadata_refresh_at=? WHERE id=?`, raw, id); err != nil {
			t.Fatal(err)
		}
	}
	set(garbage.ID, "not a time")
	set(legacyOld.ID, "2026-10-10 08:00:00 +0000 UTC")
	set(legacyFresh.ID, "2026-10-14 06:00:00 +0000 UTC")
	if err := repo.StampMetadataRefresh(ctx, canonicalOlder.ID, now.Add(-200*time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListMetadataRefreshDue(ctx, now, cutoff, 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	want := []int64{garbage.ID, canonicalOlder.ID, legacyOld.ID}
	if len(ids) != len(want) {
		t.Fatalf("due = %v, want %v (garbage first as never refreshed, fresh legacy stamp excluded)", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("due = %v, want %v", ids, want)
		}
	}
}

func TestListMetadataRefreshDue_EqualStampsBreakTiesByID(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	same := now.Add(-48 * time.Hour)

	a := newDiscoveryTestAuthor(t, repo, "OL1A", nil)
	b := newDiscoveryTestAuthor(t, repo, "OL2A", nil)
	c := newDiscoveryTestAuthor(t, repo, "OL3A", nil)
	for _, id := range []int64{c.ID, a.ID, b.ID} {
		if err := repo.StampMetadataRefresh(ctx, id, same); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.ListMetadataRefreshDue(ctx, now, now.Add(-24*time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("got %v, want the two lowest ids in order", got)
	}
}

// The stamp is not an edit: updated_at must not move.
func TestStampMetadataRefresh_LeavesUpdatedAt(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewAuthorRepo(database)
	a := newDiscoveryTestAuthor(t, repo, "OL1A", nil)
	before, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.StampMetadataRefresh(ctx, a.ID, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("updated_at moved from %v to %v", before.UpdatedAt, after.UpdatedAt)
	}
	if err := repo.StampMetadataRefresh(ctx, 99999, time.Now()); err != nil {
		t.Fatalf("stamping an unknown author is a no-op, got %v", err)
	}
}
