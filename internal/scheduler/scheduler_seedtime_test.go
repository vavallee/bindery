package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestResolveSeedLimits_SeedTimes: the grab path resolves the two seed time
// overrides (#2206) from the same indexer lookup as the ratio, and an indexer
// without them yields nil so the client keeps its own rule.
func TestResolveSeedLimits_SeedTimes(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	indexers := db.NewIndexerRepo(database)

	seed, inactive := 4320, 90
	timed := &models.Indexer{
		Name: "timed", Type: "torznab", URL: "http://t", APIKey: "k", Categories: []int{},
		SeedRatio: ptrFloat(1), SeedTimeMinutes: &seed, InactiveSeedTimeMinutes: &inactive,
	}
	if err := indexers.Create(ctx, timed); err != nil {
		t.Fatalf("create: %v", err)
	}
	plain := &models.Indexer{Name: "plain", Type: "torznab", URL: "http://p", APIKey: "k", Categories: []int{}}
	if err := indexers.Create(ctx, plain); err != nil {
		t.Fatalf("create: %v", err)
	}

	s := &Scheduler{indexers: indexers}
	got := s.resolveSeedLimits(ctx, timed.ID)
	if got.Ratio == nil || *got.Ratio != 1 {
		t.Errorf("Ratio = %v, want 1", got.Ratio)
	}
	if got.SeedTimeMinutes == nil || *got.SeedTimeMinutes != 4320 {
		t.Errorf("SeedTimeMinutes = %v, want 4320", got.SeedTimeMinutes)
	}
	if got.InactiveSeedTimeMinutes == nil || *got.InactiveSeedTimeMinutes != 90 {
		t.Errorf("InactiveSeedTimeMinutes = %v, want 90", got.InactiveSeedTimeMinutes)
	}

	none := s.resolveSeedLimits(ctx, plain.ID)
	if none.Ratio != nil || none.SeedTimeMinutes != nil || none.InactiveSeedTimeMinutes != nil {
		t.Errorf("plain indexer: %+v, want all nil", none)
	}
	if empty := (&Scheduler{}).resolveSeedLimits(ctx, timed.ID); empty.SeedTimeMinutes != nil {
		t.Errorf("nil repo: SeedTimeMinutes = %v, want nil", *empty.SeedTimeMinutes)
	}
}
