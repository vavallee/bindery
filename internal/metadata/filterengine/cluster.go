package filterengine

import (
	"sort"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// ClusterKey is the in-memory grouping key clustering uses to decide "these
// records are the same work seen through different providers/entries" for
// the lifetime of one author sync. It is deliberately NOT
// indexer.CanonicalDedupKey used alone: that key is persisted in
// books.dedup_key, is the sole signal binding the same work across
// Calibre/ABS/CWA/manual imports (#940), and carries a revision guard
// (CanonicalDedupKeyRev) with a startup backfill keyed to it. Coupling
// cluster membership to it directly would mean a future dedup-key revision
// silently reshapes every cluster — and therefore every cluster-derived
// score — in a way no scoring test would predict.
//
// This key builds on CanonicalDedupKey (so there remains exactly one title
// normalizer, not two forks of it — see #2032's own restraint on this point)
// and additionally folds a leading article via metadata.StripLeadingArticle,
// the same regex aggregator_author_works.go's articleInsensitiveTitleKey
// composition uses (which already has production mileage in
// pruneAuthorWorkRedundantTitles) — exactly one normalizer implementation,
// not a second copy of the pattern (#2235 rework). Never persisted;
// recomputed fresh every sync.
func ClusterKey(title string) string {
	return metadata.StripLeadingArticle(indexer.CanonicalDedupKey(title))
}

// Cluster is one group of candidates sharing a ClusterKey within one author
// sync. No v1 signal reads a Cluster (see engine.go's package doc) — this
// exists as the explicit pipeline stage the highest-value finding in the
// dataset behind #2235 depends on:
//
// Per-record, edition_count does not discriminate a confirmed-real work from
// misattributed or compilation noise — fiction-author-dataset's measurement
// found all three sit around 61-65% with edition_count <= 1 taken record by
// record. Aggregated as max(edition_count) over records sharing a
// ClusterKey, the same dataset found 92% of real clusters retained at a >= 3
// gate versus 90% of noise clusters dropped. The discriminator only exists
// at the cluster level, which is why clustering has to be a real pipeline
// stage computed once per sync rather than a per-record lookup — and why
// this package builds it now even though no v1 signal consumes it yet: it
// is the prerequisite for the first graded signal this engine is expected to
// gain, not speculative infrastructure.
//
// (fiction-author-dataset, github.com/gchahcg/fiction-author-dataset,
// CC-BY-4.0, ARCHITECTURE.md.)
type Cluster struct {
	Key  string
	Size int
	// MaxEditionCount is the maximum models.Book.EditionCount across every
	// member of this cluster. Requires OpenLibrary's searchAuthorWorks to
	// actually populate EditionCount on the books it returns — see
	// internal/metadata/openlibrary/client.go's fix alongside this package.
	// Zero for a cluster whose members never had EditionCount populated
	// (e.g. every member came from a provider other than OpenLibrary, or
	// predates the fix), never a sentinel — a future signal reading this
	// must treat 0 as "unknown/not applicable", not "definitely noise".
	MaxEditionCount int
	// CanonicalID is the ForeignID of the member with the highest
	// EditionCount; ties keep whichever member was seen first, matching this
	// package's overall "prefer determinism over cleverness" stance for
	// anything that isn't itself a scored signal.
	CanonicalID string
}

// BuildClusters groups books by ClusterKey in one O(n) pass and returns both
// the clusters and a lookup from each book's ForeignID to its cluster. The
// map is what lets a per-candidate loop attach the right *Cluster to each
// Candidate without a second pass or a per-candidate re-scan of the whole
// slice — the reference JS implementation (pull/lib/engine.js) measured
// roughly 10x wall time for 8x records before switching to exactly this
// build-once-then-index shape, and
// internal/metadata/aggregator_author_works.go's pruneAuthorWorkRedundantTitles
// already independently arrived at the same "build a map once" discipline
// for a closely related problem.
func BuildClusters(books []models.Book) (clusters []Cluster, byForeignID map[string]*Cluster) {
	type building struct {
		cluster     Cluster
		canonicalEC int
		// seeded is deliberately a separate bool from "cluster.CanonicalID ==
		// ''": a member seen first with an empty ForeignID is a real,
		// already-seeded value, not "no canonical member chosen yet". Using
		// emptiness as the seeded-check let a later member silently
		// re-seed canonicalEC/CanonicalID outside the normal `>` comparison
		// whenever the first-seen member's ForeignID happened to be empty.
		seeded bool
	}
	order := make([]string, 0, len(books))
	byKey := make(map[string]*building, len(books))

	for _, b := range books {
		key := ClusterKey(b.Title)
		bld, ok := byKey[key]
		if !ok {
			bld = &building{cluster: Cluster{Key: key}}
			byKey[key] = bld
			order = append(order, key)
		}
		bld.cluster.Size++
		if !bld.seeded {
			bld.cluster.CanonicalID = b.ForeignID
			bld.canonicalEC = b.EditionCount
			bld.seeded = true
		}
		if b.EditionCount > bld.cluster.MaxEditionCount {
			bld.cluster.MaxEditionCount = b.EditionCount
		}
		if b.EditionCount > bld.canonicalEC {
			bld.canonicalEC = b.EditionCount
			bld.cluster.CanonicalID = b.ForeignID
		}
	}

	clusters = make([]Cluster, 0, len(order))
	for _, key := range order {
		clusters = append(clusters, byKey[key].cluster)
	}
	// Deterministic iteration order for callers that log or test against
	// clusters directly, independent of the map's own (random) order.
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Key < clusters[j].Key })

	result := make(map[string]*Cluster, len(books))
	clusterByKey := make(map[string]*Cluster, len(clusters))
	for i := range clusters {
		clusterByKey[clusters[i].Key] = &clusters[i]
	}
	for _, b := range books {
		key := ClusterKey(b.Title)
		result[b.ForeignID] = clusterByKey[key]
	}
	return clusters, result
}
