package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestListSeriesByItem_ScopesToSourceLibraryItemAndSeriesOnly(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewABSProvenanceRepo(database)

	rows := []models.ABSProvenance{
		{SourceID: "a", LibraryID: "lib", EntityType: "series", ExternalID: "s-second", LocalID: 12, ItemID: "item"},
		{SourceID: "a", LibraryID: "lib", EntityType: "series", ExternalID: "s-first", LocalID: 11, ItemID: "item"},
		{SourceID: "a", LibraryID: "lib", EntityType: "book", ExternalID: "b", LocalID: 5, ItemID: "item"},
		{SourceID: "b", LibraryID: "lib", EntityType: "series", ExternalID: "s-other-source", LocalID: 13, ItemID: "item"},
		{SourceID: "a", LibraryID: "other-lib", EntityType: "series", ExternalID: "s-other-lib", LocalID: 14, ItemID: "item"},
		{SourceID: "a", LibraryID: "lib", EntityType: "series", ExternalID: "s-other-item", LocalID: 15, ItemID: "other-item"},
	}
	for i := range rows {
		if err := repo.Upsert(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.ListSeriesByItem(ctx, "a", "lib", "item")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].LocalID != 12 || got[1].LocalID != 11 {
		t.Fatalf("got %+v, want the two series rows of that item in insertion order", got)
	}
	for _, p := range got {
		if p.EntityType != "series" || p.SourceID != "a" || p.LibraryID != "lib" || p.ItemID != "item" {
			t.Fatalf("leaked row %+v", p)
		}
	}

	none, err := repo.ListSeriesByItem(ctx, "a", "lib", "missing")
	if err != nil || len(none) != 0 {
		t.Fatalf("missing item = %v, %v; want empty", none, err)
	}
}

func TestListSeriesByItem_ClosedDatabaseNamesTheItem(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	repo := NewABSProvenanceRepo(database)
	database.Close()
	_, err = repo.ListSeriesByItem(context.Background(), "a", "lib", "item-xyz")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "item-xyz") {
		t.Fatalf("error %q should name the item", got)
	}
}

func TestABSSeriesMatchRepo_ScopedByAllThreeKeys(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewABSSeriesMatchRepo(database)

	zone := time.FixedZone("x", 5*3600)
	at := time.Date(2026, 10, 1, 17, 0, 0, 0, zone)
	if err := repo.Upsert(ctx, &models.ABSSeriesMatchState{SourceID: "a", LibraryID: "l", ItemID: "i", InputHash: "h", Outcome: "miss"}, at); err != nil {
		t.Fatal(err)
	}
	for _, key := range [][3]string{{"b", "l", "i"}, {"a", "m", "i"}, {"a", "l", "j"}} {
		if got, err := repo.Get(ctx, key[0], key[1], key[2]); err != nil || got != nil {
			t.Fatalf("Get%v = %+v, %v; want nil, nil", key, got, err)
		}
	}
	got, err := repo.Get(ctx, "a", "l", "i")
	if err != nil || got == nil || !got.CheckedAt.Equal(at) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
}

func TestABSSeriesMatchRepo_ClosedDatabaseErrors(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	repo := NewABSSeriesMatchRepo(database)
	database.Close()
	ctx := context.Background()
	if got, err := repo.Get(ctx, "a", "l", "i"); err == nil || got != nil {
		t.Fatalf("Get = %v, %v; want an error and no state", got, err)
	}
	if err := repo.Upsert(ctx, &models.ABSSeriesMatchState{SourceID: "a", LibraryID: "l", ItemID: "i"}, time.Now()); err == nil {
		t.Fatal("Upsert on a closed database returned no error")
	}
}
