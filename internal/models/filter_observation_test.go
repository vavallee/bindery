package models

import "testing"

func TestFindObservation(t *testing.T) {
	obs := []FilterObservation{
		{Signal: "junk.titleEmptyOrAuthorName", Reason: "empty title"},
		{Signal: "language.notAllowed", Reason: "fre not allowed"},
	}
	got, ok := FindObservation(obs, "language.notAllowed")
	if !ok {
		t.Fatal("FindObservation ok = false, want true")
	}
	if got.Reason != "fre not allowed" {
		t.Errorf("FindObservation Reason = %q, want %q", got.Reason, "fre not allowed")
	}

	if _, ok := FindObservation(obs, "structure.partBookTitle"); ok {
		t.Error("FindObservation ok = true for a signal not present, want false")
	}
}

func TestHasObservation(t *testing.T) {
	obs := []FilterObservation{{Signal: SignalProviderOpenLibraryNoise}}
	if !HasObservation(obs, SignalProviderOpenLibraryNoise) {
		t.Error("HasObservation = false, want true")
	}
	if HasObservation(obs, "language.notAllowed") {
		t.Error("HasObservation = true for a signal not present, want false")
	}
	if HasObservation(nil, SignalProviderOpenLibraryNoise) {
		t.Error("HasObservation(nil, ...) = true, want false")
	}
}

func TestFilterObservation_Contribution(t *testing.T) {
	o := FilterObservation{Weight: -1000, Confidence: 1}
	if got := o.Contribution(); got != -1000 {
		t.Errorf("Contribution() = %v, want -1000", got)
	}

	o = FilterObservation{Weight: -260, Confidence: 0.35}
	if got := o.Contribution(); got != -91 {
		t.Errorf("Contribution() = %v, want -91", got)
	}
}

func TestFilterObservation_Direction(t *testing.T) {
	tests := []struct {
		weight float64
		want   string
	}{
		{-1000, "exclude"},
		{300, "keep"},
		{0, ""},
	}
	for _, tt := range tests {
		o := FilterObservation{Weight: tt.weight}
		if got := o.Direction(); got != tt.want {
			t.Errorf("Direction() with Weight=%v = %q, want %q", tt.weight, got, tt.want)
		}
	}
}
