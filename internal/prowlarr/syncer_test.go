package prowlarr

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// fakeIndexerStore records Create calls so we can inspect what was stored.
type fakeIndexerStore struct {
	created []*models.Indexer
	listed  []models.Indexer
}

func (f *fakeIndexerStore) ListByProwlarrInstance(_ context.Context, _ int64) ([]models.Indexer, error) {
	return f.listed, nil
}

func (f *fakeIndexerStore) Create(_ context.Context, idx *models.Indexer) error {
	f.created = append(f.created, idx)
	return nil
}

func (f *fakeIndexerStore) Update(_ context.Context, _ *models.Indexer) error { return nil }
func (f *fakeIndexerStore) Delete(_ context.Context, _ int64) error           { return nil }

// fakeInstanceStore is a no-op InstanceStore.
type fakeInstanceStore struct{}

func (f *fakeInstanceStore) SetLastSyncAt(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

// TestSyncDefaultCategoriesNoParent verifies that when Prowlarr sends an indexer
// with no categories, the syncer stores []int{7020} (not []int{7000,7020}).
// This is the core invariant of fix #344.
func TestSyncDefaultCategoriesNoParent(t *testing.T) {
	ctx := context.Background()
	store := &fakeIndexerStore{}
	instances := &fakeInstanceStore{}

	infos := []IndexerInfo{
		{
			ProwlarrID:     1,
			Name:           "TestIndexer",
			Protocol:       "torrent",
			TorznabURL:     "http://prowlarr/1/api",
			APIKey:         "key",
			SupportsSearch: true,
			Categories:     nil, // Prowlarr sent no categories
		},
	}

	s := &Syncer{client: nil, indexers: store, instances: instances}
	_, err := s.reconcile(ctx, 42, infos)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(store.created) != 1 {
		t.Fatalf("expected 1 indexer created, got %d", len(store.created))
	}

	cats := store.created[0].Categories
	if len(cats) != 1 || cats[0] != 7020 {
		t.Errorf("default categories = %v, want [7020] (no broad parent 7000)", cats)
	}

	// Explicit invariant: 7000 must never appear.
	for _, c := range cats {
		if c == 7000 {
			t.Errorf("parent category 7000 must not appear in synced indexer categories, got %v", cats)
		}
	}
}

// TestSyncPreservesExplicitCategories verifies that when Prowlarr does send
// explicit categories, they are stored as-is (the syncer does not inject 7000).
func TestSyncPreservesExplicitCategories(t *testing.T) {
	ctx := context.Background()
	store := &fakeIndexerStore{}
	instances := &fakeInstanceStore{}

	infos := []IndexerInfo{
		{
			ProwlarrID: 2,
			Name:       "ExplicitCats",
			TorznabURL: "http://prowlarr/2/api",
			APIKey:     "key",
			Categories: []int{7020, 7030},
		},
	}

	s := &Syncer{client: nil, indexers: store, instances: instances}
	_, err := s.reconcile(ctx, 42, infos)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(store.created) != 1 {
		t.Fatalf("expected 1 indexer created, got %d", len(store.created))
	}

	cats := store.created[0].Categories
	if len(cats) != 2 || cats[0] != 7020 || cats[1] != 7030 {
		t.Errorf("explicit categories = %v, want [7020 7030]", cats)
	}
}
