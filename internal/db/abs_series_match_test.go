package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestABSSeriesMatchRepo_GetUpsert(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo := NewABSSeriesMatchRepo(database)
	ctx := context.Background()

	if got, err := repo.Get(ctx, "default", "lib", "item"); err != nil || got != nil {
		t.Fatalf("Get on empty = %+v, %v, want nil, nil", got, err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	state := &models.ABSSeriesMatchState{SourceID: "default", LibraryID: "lib", ItemID: "item", InputHash: "h1", Outcome: "miss"}
	if err := repo.Upsert(ctx, state, at); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := repo.Get(ctx, "default", "lib", "item")
	if err != nil || got == nil || got.InputHash != "h1" || got.Outcome != "miss" || !got.CheckedAt.Equal(at) {
		t.Fatalf("Get = %+v, %v, want the stored miss", got, err)
	}

	state.InputHash, state.Outcome = "h2", "matched"
	later := at.Add(time.Hour)
	if err := repo.Upsert(ctx, state, later); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	got, _ = repo.Get(ctx, "default", "lib", "item")
	if got == nil || got.InputHash != "h2" || got.Outcome != "matched" || !got.CheckedAt.Equal(later) {
		t.Fatalf("Get after overwrite = %+v, want one row replaced", got)
	}
	if other, _ := repo.Get(ctx, "default", "other-lib", "item"); other != nil {
		t.Fatalf("Get for another library = %+v, want nil", other)
	}
}
