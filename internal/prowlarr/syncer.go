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
		// Skip indexers that aren't book-relevant. Issue #675: previously
		// every indexer Prowlarr returned was created in Bindery, including
		// ones disabled in Prowlarr, ones that don't support search, and
		// ones with no ebook/audiobook categories. Users then deleted them
		// manually and watched them reappear on the next sync.
		cats := filterCategoriesForMedia(ri.Categories)
		switch {
		case !ri.Enable:
			slog.Debug("prowlarr sync: skipping disabled indexer",
				"name", ri.Name, "prowlarr_id", ri.ProwlarrID)
			continue
		case !ri.SupportsSearch:
			slog.Debug("prowlarr sync: skipping indexer with no search support",
				"name", ri.Name, "prowlarr_id", ri.ProwlarrID)
			continue
		case len(cats) == 0:
			slog.Debug("prowlarr sync: skipping indexer with no book/audiobook categories",
				"name", ri.Name, "prowlarr_id", ri.ProwlarrID,
				"categories", ri.Categories)
			continue
		}
		seen[ri.ProwlarrID] = struct{}{}

		pID := ri.ProwlarrID
		instID := instanceID
		idxType := indexerTypeForProtocol(ri.Protocol)

		if ex, ok := byProwlarrID[ri.ProwlarrID]; ok {
			// Auto-populate the seed-ratio override from Prowlarr (#1065), but
			// only on rows the user has not taken ownership of. An explicit
			// user value/clear (source="user") always wins and is never touched;
			// an unset or previously Prowlarr-sourced row tracks Prowlarr's
			// current ratio so a later Prowlarr change refreshes it.
			ratioChanged := applyProwlarrSeedRatio(ex, ri.SeedRatio)
			// Update only if something meaningful changed. Type is included so
			// rows created by older versions (which hardcoded "torznab" for
			// every indexer, misrouting usenet grabs to torrent clients) are
			// corrected on the next sync. Categories are included so that
			// re-syncing propagates removed parent categories (7000, 3000).
			if ratioChanged || ex.Name != ri.Name || ex.URL != ri.TorznabURL || ex.Type != idxType || !intSliceEqual(ex.Categories, cats) {
				ex.Name = ri.Name
				ex.URL = ri.TorznabURL
				ex.Type = idxType
				ex.Categories = cats
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
			Type:               idxType,
			URL:                ri.TorznabURL,
			APIKey:             ri.APIKey,
			Categories:         cats,
			Priority:           25,
			Enabled:            true,
			SupportsSearch:     ri.SupportsSearch,
			ProwlarrInstanceID: &instID,
			ProwlarrIndexerID:  &pID,
		}
		applyProwlarrSeedRatio(idx, ri.SeedRatio)
		if err := s.indexers.Create(ctx, idx); err != nil {
			slog.Warn("prowlarr sync: create indexer failed",
				"name", ri.Name, "error", err)
		} else {
			result.Added++
		}
	}

	// Remove indexers that disappeared from Prowlarr — but never when a sync
	// matched nothing at all. Issue #763: a category-filter regression filtered
	// out every indexer, turning each sync into a full wipe of the user's
	// indexer config (priorities, enable state). Zero matches against a
	// non-empty existing set is treated as a failed sync, not an instruction to
	// delete everything; a genuinely stale indexer can still be removed by hand.
	if len(seen) == 0 && len(byProwlarrID) > 0 {
		slog.Warn("prowlarr sync: zero indexers matched an existing set; skipping removals to protect indexer config",
			"instance_id", instanceID, "existing", len(byProwlarrID))
	} else {
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
	}

	if err := s.instances.SetLastSyncAt(ctx, instanceID, time.Now()); err != nil {
		slog.Warn("prowlarr: persist sync timestamp", "error", err, "instance_id", instanceID)
	}
	slog.Info("prowlarr sync complete", "instance_id", instanceID, "result", result.String())
	return result, nil
}

// indexerTypeForProtocol maps a Prowlarr indexer protocol ("usenet" or
// "torrent") to the Bindery Indexer.Type the searcher uses to derive the
// release protocol. Unknown/empty values default to "torznab" to preserve the
// historical behavior for torrent-only Prowlarr setups.
func indexerTypeForProtocol(protocol string) string {
	if protocol == "usenet" {
		return "newznab"
	}
	return "torznab"
}

// filterCategoriesForMedia normalises the Newznab category list at sync time.
// Broad parent categories (7000 Other, 3000 Audio) are dropped when specific
// children are already present. When only the parent is present (no children),
// it is widened to its most useful specific child: 7000→7020 (Ebooks),
// 3000→3030 (Audiobooks). All other categories pass through unchanged.
func filterCategoriesForMedia(cats []int) []int {
	var has7000, has3000, hasChild7, hasChild3 bool
	for _, c := range cats {
		switch {
		case c == 7000:
			has7000 = true
		case c == 3000:
			has3000 = true
		case c > 7000 && c < 8000:
			hasChild7 = true
		case c > 3000 && c < 4000:
			hasChild3 = true
		}
	}

	out := make([]int, 0, len(cats))
	for _, c := range cats {
		if c != 7000 && c != 3000 {
			out = append(out, c)
		}
	}
	if has7000 && !hasChild7 {
		out = append(out, 7020)
	}
	if has3000 && !hasChild3 {
		out = append(out, 3030)
	}
	return out
}

// applyProwlarrSeedRatio reconciles an indexer's seed-ratio override with the
// ratio Prowlarr reports, returning whether the row changed.
//
// Precedence (#1065): an explicit user override always wins. A row whose
// SeedRatioSource is "user" is never touched, so a value the user set, cleared
// to null, or toggled to the -1 unlimited sentinel sticks across syncs.
//
// For an unset row, or one previously auto-populated from Prowlarr, the override
// tracks Prowlarr's current ratio: a later Prowlarr change refreshes the value,
// and a ratio removed in Prowlarr clears Bindery's auto-populated value. The
// nullable + -1-unlimited semantics from #1061 are preserved: Prowlarr only ever
// reports a positive ratio or none, so auto-population sets a concrete value or
// nil (download client keeps its global rule) — the -1 sentinel is reserved for
// the user-driven "unlimited" toggle.
func applyProwlarrSeedRatio(idx *models.Indexer, prowlarrRatio *float64) bool {
	if idx.SeedRatioSource == models.SeedRatioSourceUser {
		return false
	}
	if float64PtrEqual(idx.SeedRatio, prowlarrRatio) &&
		idx.SeedRatioSource == sourceForRatio(prowlarrRatio) {
		return false
	}
	idx.SeedRatio = prowlarrRatio
	idx.SeedRatioSource = sourceForRatio(prowlarrRatio)
	return true
}

// sourceForRatio is the provenance to record when auto-populating from Prowlarr:
// "prowlarr" when a ratio is present, "unset" when Prowlarr has none (so the row
// stays eligible for future auto-population).
func sourceForRatio(ratio *float64) string {
	if ratio == nil {
		return models.SeedRatioSourceUnset
	}
	return models.SeedRatioSourceProwlarr
}

func float64PtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
