package api

import (
	"math"

	"github.com/vavallee/bindery/internal/metadata/filterengine"
	"github.com/vavallee/bindery/internal/models"
)

// authorSyncCounters accumulates every fetchAuthorBooks outcome in one place
// (#2235 rework). Before this, the Skipped* tallies were separate local
// variables and each filterengine.Decide call site incremented its own by
// hand, immediately after deciding EXCLUDE and `continue`-ing — which meant a
// candidate that tripped more than one filter only ever recorded the first
// one a given call site happened to check, the exact bug this file exists to
// close. Now a candidate's full accumulated ledger (every applicable signal,
// evaluated together — see evaluateCandidate) is banded once, and
// signalCounter below picks the single counter to increment from that
// ledger's strongest observation.
type authorSyncCounters struct {
	added, matched, failed int

	skippedLang, skippedJunk, skippedMediaType int
	skippedNotAccepted, skippedExcluded        int
	skippedPartBooks, skippedMissingDate       int
	skippedMinPages, skippedMissingISBN        int
	// skippedThinCluster is #2235 Phase 2's counter (migration 087): a work
	// dropped by ClusterEditionCountSignal's exclude branch. Always zero for
	// a profile on the default "off" ClusterFilterPreset.
	skippedThinCluster int

	skippedLangSample        []models.AuthorSyncSkippedBook
	skippedPartBooksSample   []models.AuthorSyncSkippedBook
	skippedMissingDateSample []models.AuthorSyncSkippedBook
	skippedMinPagesSample    []models.AuthorSyncSkippedBook
	skippedMissingISBNSample []models.AuthorSyncSkippedBook
	skippedThinClusterSample []models.AuthorSyncSkippedBook
}

// addSample appends b (with obs's Reason attached) to *sample, capped at
// authorSyncSkippedSampleLimit — the same cap and reasoning every Skipped*
// sample list has used since #1889: enough titles to tell a mis-set filter
// from a correctly-set one, without letting a prolific author's rejected
// tail bloat the sync payload.
func addSample(sample *[]models.AuthorSyncSkippedBook, b models.Book, obs models.FilterObservation) {
	if len(*sample) >= authorSyncSkippedSampleLimit {
		return
	}
	*sample = append(*sample, models.AuthorSyncSkippedBook{Title: b.Title, Language: b.Language, Reason: obs.Reason})
}

// signalCounter maps every filterengine signal ID to the counting/sampling it
// drives, so an EXCLUDE-banded candidate's strongest observation (see
// strongestObservation) picks its counter directly from the ledger — never
// from whichever individual Decide call historically happened to run and
// continue first. TestEverySignalHasACounter pins that every signal
// filterengine.DefaultSignals() ships has an entry here; a signal added to
// the registry without a matching entry here fails that test rather than
// silently landing in no counter (and breaking AuthorSyncSummary.Unaccounted
// reconciliation) at runtime.
var signalCounter = map[string]func(*authorSyncCounters, models.Book, models.FilterObservation){
	"mediatype.strictMismatch": func(c *authorSyncCounters, _ models.Book, _ models.FilterObservation) {
		c.skippedMediaType++
	},
	// junk.titleEmptyOrAuthorName and junk.providerFlaggedNoise share
	// SkippedJunk: there was never a separate provider-noise counter (see
	// ProviderNoiseSignal's doc), and adding a tenth one for this rework
	// alone isn't warranted.
	"junk.titleEmptyOrAuthorName": func(c *authorSyncCounters, _ models.Book, _ models.FilterObservation) {
		c.skippedJunk++
	},
	"junk.providerFlaggedNoise": func(c *authorSyncCounters, _ models.Book, _ models.FilterObservation) {
		c.skippedJunk++
	},
	"language.notAllowed": func(c *authorSyncCounters, b models.Book, o models.FilterObservation) {
		c.skippedLang++
		addSample(&c.skippedLangSample, b, o)
	},
	"structure.partBookTitle": func(c *authorSyncCounters, b models.Book, o models.FilterObservation) {
		c.skippedPartBooks++
		addSample(&c.skippedPartBooksSample, b, o)
	},
	"catalog.missingReleaseDate": func(c *authorSyncCounters, b models.Book, o models.FilterObservation) {
		c.skippedMissingDate++
		addSample(&c.skippedMissingDateSample, b, o)
	},
	"catalog.missingISBN": func(c *authorSyncCounters, b models.Book, o models.FilterObservation) {
		c.skippedMissingISBN++
		addSample(&c.skippedMissingISBNSample, b, o)
	},
	"catalog.belowMinPages": func(c *authorSyncCounters, b models.Book, o models.FilterObservation) {
		c.skippedMinPages++
		addSample(&c.skippedMinPagesSample, b, o)
	},
	// cluster.editionCountSupport is #2235 Phase 2's ClusterEditionCountSignal
	// (internal/metadata/filterengine/signals_cluster.go). Only its
	// exclude-direction observation ever reaches recordExcluded — the
	// keep-direction observation, by construction, never bands a candidate
	// EXCLUDE. Never fires at all unless a profile's ClusterFilterPreset is
	// non-"off" (filterengine.ClusterSignalForPreset returns nil otherwise,
	// and authors.go never appends a nil signal).
	"cluster.editionCountSupport": func(c *authorSyncCounters, b models.Book, o models.FilterObservation) {
		c.skippedThinCluster++
		addSample(&c.skippedThinClusterSample, b, o)
	},
}

// strongestObservation returns the observation with the largest-magnitude
// Contribution() in obs, breaking ties by earliest position. At v1 every
// signal fires at the same veto magnitude, so a tie is the common case, and
// registry.go's doc is explicit that registry order is kept identical to the
// pre-#2235 boolean chain's check order specifically so this earliest-wins
// tie-break reproduces first-drop-wins exactly for the golden parity test —
// not by accident, by construction. A future graded signal with a genuinely
// larger magnitude would win outright, no tie involved.
func strongestObservation(obs []models.FilterObservation) (models.FilterObservation, bool) {
	if len(obs) == 0 {
		return models.FilterObservation{}, false
	}
	strongest := obs[0]
	strongestMag := math.Abs(strongest.Contribution())
	for _, o := range obs[1:] {
		if mag := math.Abs(o.Contribution()); mag > strongestMag {
			strongest = o
			strongestMag = mag
		}
	}
	return strongest, true
}

// recordExcluded attributes an EXCLUDE-banded candidate to exactly one
// Skipped* counter (plus that counter's sample, where it has one) via
// signalCounter, keyed on the accumulated ledger's strongestObservation. b is
// the candidate book (for Title/Language on the sample), obs the full fired
// observation list.
//
// Panics on an observation whose Signal has no signalCounter entry — that is
// a programming error (a registry signal shipped without wiring its counter)
// that TestEverySignalHasACounter exists specifically to catch at build time
// instead, so reaching this at runtime means that test was bypassed or is
// itself broken.
func recordExcluded(c *authorSyncCounters, b models.Book, obs []models.FilterObservation) {
	strongest, ok := strongestObservation(obs)
	if !ok {
		return
	}
	fn, ok := signalCounter[strongest.Signal]
	if !ok {
		panic("filterengine: signal " + strongest.Signal + " fired with no signalCounter entry — see TestEverySignalHasACounter")
	}
	fn(c, b, strongest)
}

// filterEngineSignals returns the canonical v1 signal set (literally
// filterengine.DefaultSignals(), the same construction TestRegistryIsVetoOnlyAtV1
// checks) split into the two evaluation passes fetchAuthorBooks needs,
// indexed by ID within each.
//
// This is the fix for the other half of #2235's original gap: every call
// site used to build its own signal list with ad-hoc filterengine.New*Signal()
// calls, so a signal added to DefaultSignals() (and therefore covered by
// TestRegistryIsVetoOnlyAtV1) had no guarantee production ever constructed
// it at all. Sourcing both passes from one DefaultSignals() call closes that:
// a signal the registry ships is a signal production evaluates, full stop.
//
// freeSignals (mediatype, junk title, provider noise, language) run over
// every candidate before any provider round trip. structureSignals
// (part book, missing date) and editionSignals (missing ISBN, min pages)
// run only for candidates that survive freeSignals and have no existing
// library row — editionSignals additionally only when that candidate's
// edition lookup actually succeeded, matching the exact call-site gating
// MissingISBNSignal and MinPagesSignal's own docs require.
func filterEngineSignals() (free, structure, edition []filterengine.Signal) {
	for _, s := range filterengine.DefaultSignals() {
		switch s.ID() {
		case "mediatype.strictMismatch", "junk.titleEmptyOrAuthorName", "junk.providerFlaggedNoise", "language.notAllowed":
			free = append(free, s)
		case "structure.partBookTitle", "catalog.missingReleaseDate":
			structure = append(structure, s)
		case "catalog.missingISBN", "catalog.belowMinPages":
			edition = append(edition, s)
		}
	}
	return free, structure, edition
}
