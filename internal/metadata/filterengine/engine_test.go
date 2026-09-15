package filterengine

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestBandFor(t *testing.T) {
	tests := []struct {
		name             string
		score            float64
		keepThreshold    float64
		excludeThreshold float64
		want             Band
	}{
		{"clean record at parity default", 0, 0, 0, BandKeep},
		{"one veto fired at parity default", -vetoWeight, 0, 0, BandExclude},
		{"three vetoes fired at parity default", -3 * vetoWeight, 0, 0, BandExclude},
		{"exactly on the exclude boundary is review, not excluded", -10, 0, -10, BandReview},
		{"just below the exclude boundary is excluded", -10.0001, 0, -10, BandExclude},
		{"inside an open review band", -5, 10, -10, BandReview},
		{"exactly on the keep boundary is keep, not review", 10, 10, -10, BandKeep},
		{"just below the keep boundary is review", 9.9999, 10, -10, BandReview},
		{"keep == exclude collapses review to empty, just above", 0.0001, 0, 0, BandKeep},
		{"keep == exclude collapses review to empty, just below", -0.0001, 0, 0, BandExclude},
	}
	for _, tt := range tests {
		if got := BandFor(tt.score, tt.keepThreshold, tt.excludeThreshold); got != tt.want {
			t.Errorf("%s: BandFor(%v, keep=%v, exclude=%v) = %v, want %v",
				tt.name, tt.score, tt.keepThreshold, tt.excludeThreshold, got, tt.want)
		}
	}
}

func TestDecide_EmptyLedgerScoresAtPrior(t *testing.T) {
	ctx := &Context{Prior: 0, KeepThreshold: 0, ExcludeThreshold: 0}
	result := Decide(Candidate{Book: &models.Book{Title: "Clean Title"}}, ctx)
	if result.Score != 0 {
		t.Errorf("score = %v, want 0", result.Score)
	}
	if result.Band != BandKeep {
		t.Errorf("band = %v, want %v", result.Band, BandKeep)
	}
	if len(result.Observations) != 0 {
		t.Errorf("observations = %v, want none", result.Observations)
	}
}

func TestDecide_NonzeroPriorShiftsScore(t *testing.T) {
	ctx := &Context{Prior: 3.5, KeepThreshold: 0, ExcludeThreshold: 0}
	result := Decide(Candidate{Book: &models.Book{Title: "Clean Title"}}, ctx)
	if result.Score != 3.5 {
		t.Errorf("score = %v, want 3.5", result.Score)
	}
}

func TestDecide_SumsMultipleFiringSignals(t *testing.T) {
	ctx := &Context{
		Prior:            0,
		KeepThreshold:    0,
		ExcludeThreshold: 0,
		AllowedLanguages: []string{"eng"},
		SkipPartBooks:    true,
	}
	c := Candidate{Book: &models.Book{Title: "The Foo Boxed Set", Language: "fre"}}
	result := Decide(c, ctx, NewLanguageSignal(), NewPartBookSignal())
	if len(result.Observations) != 2 {
		t.Fatalf("observations = %d, want 2 (both language and part-book should fire)", len(result.Observations))
	}
	if result.Score != -2*vetoWeight {
		t.Errorf("score = %v, want %v", result.Score, -2*vetoWeight)
	}
	if result.Band != BandExclude {
		t.Errorf("band = %v, want %v", result.Band, BandExclude)
	}
}

func TestFilterObservation_ContributionAndDirection(t *testing.T) {
	keep := models.FilterObservation{Weight: 4, Confidence: 0.5}
	if got := keep.Contribution(); got != 2 {
		t.Errorf("Contribution() = %v, want 2", got)
	}
	if got := keep.Direction(); got != "keep" {
		t.Errorf("Direction() = %q, want %q", got, "keep")
	}

	exclude := models.FilterObservation{Weight: -vetoWeight, Confidence: 1}
	if got := exclude.Direction(); got != "exclude" {
		t.Errorf("Direction() = %q, want %q", got, "exclude")
	}

	zero := models.FilterObservation{Weight: 0, Confidence: 1}
	if got := zero.Direction(); got != "" {
		t.Errorf("Direction() = %q, want empty", got)
	}
}
