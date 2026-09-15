package api

import (
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/filterengine"
	"github.com/vavallee/bindery/internal/models"
)

// TestEverySignalHasACounter pins signalCounter's coverage of
// filterengine.DefaultSignals() — the same registry construction
// TestRegistryIsVetoOnlyAtV1 (internal/metadata/filterengine) checks. A
// signal shipped in the registry with no matching entry here would panic at
// runtime the first time it fired (see recordExcluded) instead of failing a
// build, which is exactly the kind of drift #2235's original wiring gap
// allowed: a test that guards a registry construction production never
// actually calls proves nothing about production.
func TestEverySignalHasACounter(t *testing.T) {
	for _, s := range filterengine.DefaultSignals() {
		if _, ok := signalCounter[s.ID()]; !ok {
			t.Errorf("signal %q has no signalCounter entry — a candidate excluded solely by this "+
				"signal would panic in recordExcluded instead of incrementing a Skipped* counter", s.ID())
		}
	}
}

// TestFilterEngineSignals_CoversEveryDefaultSignalExactlyOnce guards
// filterEngineSignals' free/structure/edition split: every signal
// DefaultSignals() ships must land in exactly one of the three buckets, so a
// signal added to the registry without updating this switch fails here
// instead of silently never being evaluated by fetchAuthorBooks at all.
func TestFilterEngineSignals_CoversEveryDefaultSignalExactlyOnce(t *testing.T) {
	free, structure, edition := filterEngineSignals()
	seen := map[string]int{}
	for _, bucket := range [][]filterengine.Signal{free, structure, edition} {
		for _, s := range bucket {
			seen[s.ID()]++
		}
	}
	for _, s := range filterengine.DefaultSignals() {
		if seen[s.ID()] != 1 {
			t.Errorf("signal %q appears in %d of filterEngineSignals' three buckets, want exactly 1", s.ID(), seen[s.ID()])
		}
	}
	if got, want := len(free)+len(structure)+len(edition), len(filterengine.DefaultSignals()); got != want {
		t.Errorf("filterEngineSignals returned %d signals total, want %d (DefaultSignals() length)", got, want)
	}
}

// TestEvaluateCandidate_AccumulatesEveryFiringSignal is the concrete,
// white-box proof (not prose) that a candidate tripping multiple signals in
// one Decide call gets every one of them recorded in the ledger, not just
// the first — the actual #2235 bug this rework closes. Builds a candidate
// that fires BOTH JunkTitleSignal (title equals the author's name) AND
// LanguageSignal (language outside the allowed set) in a single accumulated
// Decide call, exactly the shape fetchAuthorBooks's free-signal pass now
// uses, and asserts the Result carries both observations before banding —
// not merely that the final band is EXCLUDE, which a first-veto-wins
// implementation would also produce and which is why this needs a direct
// Observations-count assertion, not a black-box counter check.
func TestEvaluateCandidate_AccumulatesEveryFiringSignal(t *testing.T) {
	book := &models.Book{Title: "Prolix Author", Language: "fre"}
	ctx := &filterengine.Context{
		AllowedLanguages: []string{"eng"},
		NormalizedAuthor: "prolix author",
	}
	free, _, _ := filterEngineSignals()
	result := filterengine.Decide(filterengine.Candidate{Book: book}, ctx, free...)

	if result.Band != filterengine.BandExclude {
		t.Fatalf("Band = %v, want EXCLUDE", result.Band)
	}
	if len(result.Observations) != 2 {
		t.Fatalf("Observations = %d, want exactly 2 (junk.titleEmptyOrAuthorName AND language.notAllowed) — "+
			"a first-fired-wins implementation would stop at 1, which is the bug this rework fixes", len(result.Observations))
	}
	got := map[string]bool{}
	for _, o := range result.Observations {
		got[o.Signal] = true
	}
	for _, want := range []string{"junk.titleEmptyOrAuthorName", "language.notAllowed"} {
		if !got[want] {
			t.Errorf("missing observation from signal %q; got signals: %v", want, got)
		}
	}

	// The strongest-observation attribution this ledger feeds
	// (recordExcluded) must pick the one earliest in registry order among
	// the equal-magnitude vetoes — junk before language, matching the
	// pre-#2235 boolean chain's check order (registry_default.go's doc).
	strongest, ok := strongestObservation(result.Observations)
	if !ok || strongest.Signal != "junk.titleEmptyOrAuthorName" {
		t.Errorf("strongestObservation = %+v, ok=%v, want junk.titleEmptyOrAuthorName (registry-order tie-break)", strongest, ok)
	}
}

// TestRecordExcluded_AttributesStrongestObservation is a smaller, direct unit
// test of recordExcluded's counter attribution in isolation, independent of
// the full sync loop: a two-observation ledger must increment exactly one
// counter (the one signalCounter maps the strongest observation to), not
// both.
func TestRecordExcluded_AttributesStrongestObservation(t *testing.T) {
	c := &authorSyncCounters{}
	// Registry order (mediatype, junk, providernoise, language, ...) — the
	// same order filterengine.Decide would actually produce these in, since
	// it appends each signal's observations in call order. Getting this
	// backwards here would test something Decide never actually hands
	// recordExcluded, not a real regression in strongestObservation itself.
	obs := []models.FilterObservation{
		{Signal: "junk.titleEmptyOrAuthorName", Weight: -1000, Confidence: 1, Reason: "title matches author name"},
		{Signal: "language.notAllowed", Weight: -1000, Confidence: 1, Reason: "language fre not allowed"},
	}
	recordExcluded(c, models.Book{Title: "Prolix Author", Language: "fre"}, obs)

	if c.skippedJunk != 1 {
		t.Errorf("skippedJunk = %d, want 1 (junk is first in registry order, so it wins the tie)", c.skippedJunk)
	}
	if c.skippedLang != 0 {
		t.Errorf("skippedLang = %d, want 0: only the strongest observation's counter should increment", c.skippedLang)
	}
}

// TestStrongestObservation_LaterStrictlyLargerMagnitudeWins pins the branch
// TestRecordExcluded_AttributesStrongestObservation's tie case doesn't reach:
// strongestObservation's doc says a genuinely larger magnitude wins outright,
// no tie involved. Every v1 signal fires at the same veto magnitude, so this
// can't happen through the real registry today — it's exercised directly
// here as a pure function test of strongestObservation's own comparison
// logic, ready for the day a graded signal's magnitude actually varies.
func TestStrongestObservation_LaterStrictlyLargerMagnitudeWins(t *testing.T) {
	obs := []models.FilterObservation{
		{Signal: "junk.titleEmptyOrAuthorName", Weight: -1000, Confidence: 1},
		{Signal: "language.notAllowed", Weight: -1000, Confidence: 0.5},
		{Signal: "structure.partBookTitle", Weight: -2000, Confidence: 1},
	}
	got, ok := strongestObservation(obs)
	if !ok {
		t.Fatal("strongestObservation returned ok=false for a non-empty slice")
	}
	if got.Signal != "structure.partBookTitle" {
		t.Errorf("strongestObservation = %q, want structure.partBookTitle (the only -2000 magnitude, strictly larger than either -1000)", got.Signal)
	}
}

// TestStrongestObservation_Empty pins the len(obs)==0 short-circuit — no
// candidate's ledger is ever actually empty when recordExcluded is called
// (Decide only calls it when result.Band == BandExclude, which requires at
// least one observation), but the function is exported to this package and
// its own doc promises ok=false for that input.
func TestStrongestObservation_Empty(t *testing.T) {
	_, ok := strongestObservation(nil)
	if ok {
		t.Error("strongestObservation(nil) ok = true, want false")
	}
}

// TestRecordExcluded_PanicsOnUnmappedSignal pins the guard
// TestEverySignalHasACounter exists to make unreachable in practice: an
// observation whose Signal has no signalCounter entry is a programming
// error (a registry signal shipped without wiring its counter), and
// recordExcluded's own doc says it panics rather than silently dropping the
// count — reaching this is meant to be loud, not a quiet miscount.
func TestRecordExcluded_PanicsOnUnmappedSignal(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("recordExcluded did not panic for an unmapped signal ID")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "nonsense.unmappedSignal") {
			t.Errorf("panic value = %v, want a string mentioning the unmapped signal ID", r)
		}
	}()
	c := &authorSyncCounters{}
	recordExcluded(c, models.Book{Title: "X"}, []models.FilterObservation{
		{Signal: "nonsense.unmappedSignal", Weight: -1000, Confidence: 1},
	})
}
