package filterengine

// ClusterFilterPreset names one of the tuned configurations #2235 Phase 2
// validated for ClusterEditionCountSignal, offline, against
// fiction-author-dataset's 1,422 hand-verified rows across 20 authors
// (github.com/gchahcg/fiction-author-dataset, CC-BY-4.0). Deliberately a
// closed set of server-side-owned presets, not raw client-suppliable signal
// weights and thresholds — nothing about picking those numbers is something
// a user should need to understand, and a badly-chosen raw pair could put
// the engine somewhere nobody has measured.
//
// This is a separate, additive knob from the profile's own
// KeepThreshold/ExcludeThreshold columns (migration 086) — those stay locked
// to exactly 0/0 by internal/api/metadata_profiles.go's
// validateScoreThresholds, completely unchanged by this package. A non-off
// preset instead supplies its own shared threshold, applied to BOTH
// BandFor's keepThreshold and excludeThreshold arguments at the call site
// (internal/api/authors.go), never just one — see ThresholdForPreset's doc
// for why keeping the two equal, not just moving exclude_threshold down on
// its own, matters here.
type ClusterFilterPreset string

const (
	// ClusterFilterOff disables ClusterEditionCountSignal entirely and
	// leaves the effective threshold at 0 — byte-identical to every
	// profile's behavior before this preset field existed, and the default
	// for every existing profile after this migration.
	ClusterFilterOff ClusterFilterPreset = "off"

	// ClusterFilterConservative is the gentlest active tier: a weaker
	// keep-direction pull (weight 200 at MaxEditionCount>=3) and a milder
	// shared threshold (-500) than ClusterFilterBalanced, plus the same
	// exclude-direction branch (weight -600 at MaxEditionCount<=0, versus
	// -1000 for balanced) at reduced strength rather than switched off
	// outright — an earlier candidate that turned the exclude branch off
	// completely measured far worse on Nora Roberts specifically (full
	// 20-author precision 66.9% vs. this tier's 79.2%), because that branch
	// is what suppresses the noise concentrated in exactly the largest,
	// thinnest-catalogue authors, not an incidental feature to trade away
	// for a "safer" default. Measured (20 authors, Nora Roberts/J.D. Robb
	// isolated as a known, filtering-unsolvable pen-name misattribution —
	// see ClusterFilterBalanced's doc): recall=88.1%, precision=83.5% on the
	// other 19 authors.
	ClusterFilterConservative ClusterFilterPreset = "conservative"

	// ClusterFilterBalanced is the configuration #2235 Phase 2's grid search
	// actually validated as the headline result: keepWeight=300 at
	// MaxEditionCount>=3, excludeWeight=-1000 at MaxEditionCount<=0, shared
	// threshold=-750. Isolating Nora Roberts/J.D. Robb — 471 of the
	// dataset's 1,422 rows, carrying a known, filtering-unsolvable pen-name
	// misattribution (OpenLibrary attributes all of J.D. Robb's "In Death"
	// books to the Nora Roberts identity, so no per-candidate filtering
	// signal can separate them) — the remaining 19 authors scored
	// recall=90.9%, precision=83.7%. Recommended default for a profile that
	// wants this signal on at all.
	ClusterFilterBalanced ClusterFilterPreset = "balanced"

	// ClusterFilterAggressive widens both the keep gate (MaxEditionCount>=1,
	// versus >=3) and the exclude band (MaxEditionCount<=1, versus <=0), at a
	// higher keep weight (700) and a looser shared threshold (-950 — NOT
	// -1000: -1000 exactly equals vetoWeight, and BandFor's strict "<" bands
	// a lone veto's score of -1000 as KEEP rather than EXCLUDE at that exact
	// value, silently defanging every v1 signal for the whole profile. An
	// independent review pass caught this after an earlier version of this
	// preset shipped -1000 and its "recall=92.4%" citation turned out to be
	// measuring that defanged behavior, not real rescuing — re-measured
	// after the fix at -950, a value with headroom on both sides of the
	// coincidence). Trades essentially no measured precision for a further
	// recall gain on the same 19-author isolation: recall=97.0%,
	// precision=83.8% — the three tiers hold precision nearly flat on this
	// dataset and differ mainly in how much additional recall each one
	// rescues, not in a precision/recall trade a user is choosing to
	// accept; document it that way rather than the more familiar (but here
	// inaccurate) "aggressive trades precision for recall" framing.
	ClusterFilterAggressive ClusterFilterPreset = "aggressive"
)

// clusterPresetTuning holds one preset's validated (threshold, keepWeight,
// keepMinEditionCount, excludeWeight, excludeMaxEditionCount) tuple.
// threshold is bundled with the signal weights, not set independently,
// because the two were measured together — see each ClusterFilterPreset
// constant's doc for the citation.
type clusterPresetTuning struct {
	threshold                                   float64
	keepWeight, excludeWeight                   float64
	keepMinEditionCount, excludeMaxEditionCount int
}

var clusterPresetTunings = map[ClusterFilterPreset]clusterPresetTuning{
	ClusterFilterConservative: {threshold: -500, keepWeight: 200, excludeWeight: -600, keepMinEditionCount: 3, excludeMaxEditionCount: 0},
	ClusterFilterBalanced:     {threshold: -750, keepWeight: 300, excludeWeight: -1000, keepMinEditionCount: 3, excludeMaxEditionCount: 0},
	ClusterFilterAggressive:   {threshold: -950, keepWeight: 700, excludeWeight: -1000, keepMinEditionCount: 1, excludeMaxEditionCount: 1},
}

// ValidClusterFilterPreset reports whether p is a preset this package knows
// how to apply — ClusterFilterOff included. internal/api/metadata_profiles.go
// validates an incoming profile's preset against this instead of validating
// raw threshold numbers, so an API client can never put the engine in a
// configuration nobody has measured.
func ValidClusterFilterPreset(p ClusterFilterPreset) bool {
	if p == ClusterFilterOff {
		return true
	}
	_, ok := clusterPresetTunings[p]
	return ok
}

// ThresholdForPreset returns the single threshold value p ships bundled with
// — applied to BOTH Context.KeepThreshold and Context.ExcludeThreshold at
// the call site, never just ExcludeThreshold alone. Keeping the two equal
// (rather than lowering only the exclude side) is what keeps BandFor's
// REVIEW band permanently empty, exactly as it is under the v1 veto-only
// default: BandFor treats [exclude, keep) as REVIEW, and no v1 UI exists to
// show it. Widening only exclude_threshold downward while leaving
// keep_threshold at 0 would make REVIEW newly reachable for any score in
// between — a real, un-surfaced band change this package isn't shipping.
// Returns 0 for ClusterFilterOff or any unrecognized preset — 0 is also the
// v1 veto-parity value, so an unrecognized preset degrades to "acts like
// this signal is off" rather than to an untested threshold.
func ThresholdForPreset(p ClusterFilterPreset) float64 {
	return clusterPresetTunings[p].threshold
}

// ClusterSignalForPreset returns the ClusterEditionCountSignal p specifies,
// or nil for ClusterFilterOff (and any preset ValidClusterFilterPreset
// rejects) — callers must check for nil rather than appending a no-op
// signal, matching how internal/api/authors.go already treats an optional
// signal group.
func ClusterSignalForPreset(p ClusterFilterPreset) Signal {
	t, ok := clusterPresetTunings[p]
	if !ok {
		return nil
	}
	return NewClusterEditionCountSignal(t.keepWeight, t.excludeWeight, t.keepMinEditionCount, t.excludeMaxEditionCount)
}
