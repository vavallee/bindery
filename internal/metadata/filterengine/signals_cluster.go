package filterengine

import (
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

// ClusterEditionCountSignal is #2235 Phase 2's graded, bidirectional signal.
// Every v1 signal in DefaultSignals() only ever excludes; this one can also
// keep, and can independently push a candidate toward EXCLUDE on
// thin-catalogue evidence alone, with no other signal needing to fire first.
// Deliberately never added to DefaultSignals() itself — that function backs
// TestRegistryIsVetoOnlyAtV1, which pins it as pure-veto for the v1 parity
// guarantee migration 086 documents. This signal is instead constructed via
// ClusterFilterPreset (registry_cluster_preset.go) and only takes effect for
// a metadata profile that opts into a non-"off" preset.
//
// Both branches read the same underlying fact (Cluster.MaxEditionCount, see
// cluster.go's doc for why this has to be a cluster aggregate and not a
// per-record field: edition_count alone doesn't separate core from noise,
// 61-65% of every work_kind sits at <=1 taken record by record), just on
// opposite sides of it:
//
//   - keepMinEditionCount: MaxEditionCount >= this fires the KEEP-direction
//     observation. fiction-author-dataset's cited number for the >=3 gate is
//     92% of real clusters retained versus 90% of noise clusters dropped —
//     the number this package has cited since Phase 1's clustering stage was
//     first built.
//   - excludeMaxEditionCount: MaxEditionCount <= this fires the
//     EXCLUDE-direction observation. This threshold is NOT the same cited
//     figure — fiction-author-dataset's own number is KEEP-side only, and
//     nothing in it implies an equally strong EXCLUDE-side discriminator at
//     the same cutoff. Measured instead from a live 20-author OpenLibrary
//     corpus pulled during #2235 Phase 2 tuning (matched ground-truth
//     records only): MaxEditionCount == 0 is 14.5% of confirmed CORE
//     records, versus 55.7% of confirmed EXCLUSION records and 48.0% of the
//     unlabeled/unknown pull mass — a real, substantial concentration in
//     exactly the noise this signal exists to suppress, at roughly 3-4x the
//     rate it appears in real work. Strict zero (not a wider <=1 band) is
//     the better of the two: the <=1 band's core false-positive rate
//     roughly doubles (to 23.1%) for only a modest gain in exclusion/unknown
//     coverage (to 68.0%/78.7%).
//
// A cluster strictly between the two thresholds contributes nothing — this
// signal only ever grades the two tails it has real evidence for, not the
// ambiguous middle.
//
// cluster.go's MaxEditionCount doc has a real caution this signal's exclude
// branch has to take seriously: zero is documented as "unknown/not
// applicable", not "definitely noise", because OpenLibrary reporting no
// edition_count and OpenLibrary reporting a genuine edition_count of 0 are
// indistinguishable in this pipeline (internal/metadata/openlibrary/client.go
// only ever sets EditionCount when e.EditionCount > 0). This signal's
// exclude branch is an explicit, measured bet that in practice — on this
// dataset — that ambiguity resolves toward noise far more often than toward
// an under-indexed real book, not a claim that the ambiguity has been
// resolved.
//
// The bidirectional configuration (both branches active, keepWeight=300 at
// keepMinEditionCount=3, excludeWeight=-1000 at excludeMaxEditionCount=0)
// paired with exclude_threshold=-750 is what #2235 Phase 2's offline grid
// search (20 authors, fiction-author-dataset, CC-BY-4.0) validated: isolating
// the one known, filtering-unsolvable pen-name misattribution in that
// dataset (Nora Roberts/J.D. Robb — OpenLibrary attributes all of J.D.
// Robb's "In Death" books to the Nora Roberts identity), the remaining 19
// authors scored recall=90.9%/precision=83.7%, versus a lower baseline with
// this signal off. See ClusterFilterPreset's doc for the exact shipped
// mapping.
//
// Signal.Weight() returns 0 for this signal, not a real magnitude: unlike
// every other signal in this package, there is no single "the" weight to
// report — keepWeight and excludeWeight are independent, configured
// separately, and can differ in both sign and magnitude. Decide never reads
// Signal.Weight() for scoring (only the Weight field on each emitted
// FilterObservation), so this costs nothing at runtime; it only means this
// signal cannot be dropped into DefaultSignals() and checked by
// TestRegistryIsVetoOnlyAtV1's Weight()-accessor assertion, which is
// correct — it isn't a v1 veto-only signal and was never meant to pass that
// check.
type ClusterEditionCountSignal struct {
	keepWeight    float64
	excludeWeight float64

	keepMinEditionCount    int
	excludeMaxEditionCount int
}

// NewClusterEditionCountSignal returns a cluster-edition-count signal with
// independently configured keep/exclude weights and thresholds. keepWeight
// should be positive (keep-direction), excludeWeight negative
// (exclude-direction) — the signal does not enforce this, since a caller may
// legitimately want to turn one branch off entirely by setting its weight to
// 0 (see the "conservative" preset). excludeMaxEditionCount must be less
// than keepMinEditionCount or every cluster would fire both branches at
// once; this constructor does not enforce that either, for the same reason.
func NewClusterEditionCountSignal(keepWeight, excludeWeight float64, keepMinEditionCount, excludeMaxEditionCount int) *ClusterEditionCountSignal {
	return &ClusterEditionCountSignal{
		keepWeight: keepWeight, excludeWeight: excludeWeight,
		keepMinEditionCount: keepMinEditionCount, excludeMaxEditionCount: excludeMaxEditionCount,
	}
}

func (s *ClusterEditionCountSignal) ID() string      { return "cluster.editionCountSupport" }
func (s *ClusterEditionCountSignal) Weight() float64 { return 0 }

func (s *ClusterEditionCountSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil || c.Cluster == nil {
		return nil
	}
	switch {
	case c.Cluster.MaxEditionCount >= s.keepMinEditionCount:
		return []models.FilterObservation{{
			Signal:     s.ID(),
			Weight:     s.keepWeight,
			Confidence: 1,
			Reason: fmt.Sprintf("cluster %q has max edition_count=%d (>= %d), strong signal of a real catalogued work",
				c.Cluster.Key, c.Cluster.MaxEditionCount, s.keepMinEditionCount),
		}}
	case c.Cluster.MaxEditionCount <= s.excludeMaxEditionCount:
		return []models.FilterObservation{{
			Signal:     s.ID(),
			Weight:     s.excludeWeight,
			Confidence: 1,
			Reason: fmt.Sprintf("cluster %q has max edition_count=%d (<= %d), consistent with thin-catalogue noise",
				c.Cluster.Key, c.Cluster.MaxEditionCount, s.excludeMaxEditionCount),
		}}
	default:
		return nil
	}
}
