package prowlarr

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// IndexerStore is the subset of db.IndexerRepo needed by the syncer.
type IndexerStore interface {
	ListByProwlarrInstance(ctx context.Context, instanceID int64) ([]models.Indexer, error)
	Create(ctx context.Context, idx *models.Indexer) error
	Update(ctx context.Context, idx *models.Indexer) error
	Delete(ctx context.Context, id int64) error
}

// InstanceStore is the subset of db.ProwlarrRepo needed by the syncer.
type InstanceStore interface {
	SetLastSyncAt(ctx context.Context, id int64, t time.Time) error
}

// Syncer pulls indexers from Prowlarr and reconciles them with Bindery's
// indexer table. It creates new entries, updates changed ones, and deletes
// entries that no longer exist in Prowlarr.
type Syncer struct {
	client    *Client
	indexers  IndexerStore
	instances InstanceStore
}

// NewSyncer constructs a Syncer for the given Prowlarr instance.
func NewSyncer(client *Client, indexers IndexerStore, instances InstanceStore) *Syncer {
	return &Syncer{client: client, indexers: indexers, instances: instances}
}

// SyncResult summarises what changed during a sync.
type SyncResult struct {
	Added   int
	Updated int
	Removed int
}

func (r SyncResult) String() string {
	return fmt.Sprintf("added=%d updated=%d removed=%d", r.Added, r.Updated, r.Removed)
}

// Sync fetches all indexers from Prowlarr and reconciles them.
func (s *Syncer) Sync(ctx context.Context, instanceID int64) (SyncResult, error) {
	remotes, err := s.client.FetchIndexers(ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("fetch prowlarr indexers: %w", err)
	}

	existing, err := s.indexers.ListByProwlarrInstance(ctx, instanceID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("list existing prowlarr indexers: %w", err)
	}

	// Index existing by ProwlarrIndexerID for O(1) lookup.
	byProwlarrID := map[int]*models.Indexer{}
	for i := range existing {
		if existing[i].ProwlarrIndexerID != nil {
			byProwlarrID[*existing[i].ProwlarrIndexerID] = &existing[i]
		}
	}

	var result SyncResult
	seen := map[int]struct{}{}

	for _, ri := range remotes {
		seen[ri.ProwlarrID] = struct{}{}
		cats := ri.Categories
		if len(cats) == 0 {
			cats = []int{7000, 7020}
		}

		pID := ri.ProwlarrID
		instID := instanceID

		if ex, ok := byProwlarrID[ri.ProwlarrID]; ok {
			// Update only if something meaningful changed.
			if ex.Name != ri.Name || ex.URL != ri.TorznabURL {
				ex.Name = ri.Name
				ex.URL = ri.TorznabURL
				ex.SupportsSearch = ri.SupportsSearch
				if err := s.indexers.Update(ctx, ex); err != nil {
					slog.Warn("prowlarr sync: update indexer failed",
						"name", ri.Name, "error", err)
				} else {
					result.Updated++
				}
			}
			continue
		}

		// New indexer from Prowlarr.
		idx := &models.Indexer{
			Name:               ri.Name,
			Type:               "torznab",
			URL:                ri.TorznabURL,
			APIKey:             ri.APIKey,
			Categories:         cats,
			Priority:           25,
			Enabled:            true,
			SupportsSearch:     ri.SupportsSearch,
			ProwlarrInstanceID: &instID,
			ProwlarrIndexerID:  &pID,
		}
		if err := s.indexers.Create(ctx, idx); err != nil {
			slog.Warn("prowlarr sync: create indexer failed",
				"name", ri.Name, "error", err)
		} else {
			result.Added++
		}
	}

	// Remove indexers that disappeared from Prowlarr.
	for prowlarrID, ex := range byProwlarrID {
		if _, ok := seen[prowlarrID]; ok {
			continue
		}
		if err := s.indexers.Delete(ctx, ex.ID); err != nil {
			slog.Warn("prowlarr sync: delete stale indexer failed",
				"id", ex.ID, "name", ex.Name, "error", err)
		} else {
			result.Removed++
		}
	}

	_ = s.instances.SetLastSyncAt(ctx, instanceID, time.Now())
	slog.Info("prowlarr sync complete", "instance_id", instanceID, "result", result.String())
	return result, nil
}
