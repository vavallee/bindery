package filterengine

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestClusterEditionCountSignal_KeepBranch(t *testing.T) {
	s := NewClusterEditionCountSignal(300, -300, 3, 0)
	c := Candidate{Book: &models.Book{}, Cluster: &Cluster{Key: "k", MaxEditionCount: 5}}
	obs := s.Observe(c, &Context{})
	if len(obs) != 1 {
		t.Fatalf("Observe returned %d observations, want 1", len(obs))
	}
	if obs[0].Weight != 300 || obs[0].Confidence != 1 {
		t.Errorf("obs = %+v, want Weight=300 Confidence=1", obs[0])
	}
}

func TestClusterEditionCountSignal_ExcludeBranch(t *testing.T) {
	s := NewClusterEditionCountSignal(300, -300, 3, 0)
	c := Candidate{Book: &models.Book{}, Cluster: &Cluster{Key: "k", MaxEditionCount: 0}}
	obs := s.Observe(c, &Context{})
	if len(obs) != 1 {
		t.Fatalf("Observe returned %d observations, want 1", len(obs))
	}
	if obs[0].Weight != -300 || obs[0].Confidence != 1 {
		t.Errorf("obs = %+v, want Weight=-300 Confidence=1", obs[0])
	}
}

func TestClusterEditionCountSignal_NeutralMiddle(t *testing.T) {
	s := NewClusterEditionCountSignal(300, -300, 3, 0)
	for _, maxEC := range []int{1, 2} {
		c := Candidate{Book: &models.Book{}, Cluster: &Cluster{Key: "k", MaxEditionCount: maxEC}}
		if obs := s.Observe(c, &Context{}); len(obs) != 0 {
			t.Errorf("MaxEditionCount=%d: Observe returned %d observations, want 0 (ambiguous middle, no evidence either way)", maxEC, len(obs))
		}
	}
}

func TestClusterEditionCountSignal_NilClusterOrBook(t *testing.T) {
	s := NewClusterEditionCountSignal(300, -300, 3, 0)
	if obs := s.Observe(Candidate{Book: &models.Book{}, Cluster: nil}, &Context{}); len(obs) != 0 {
		t.Errorf("nil Cluster: Observe returned %d observations, want 0 (no clustering ran for this pass)", len(obs))
	}
	if obs := s.Observe(Candidate{Book: nil, Cluster: &Cluster{MaxEditionCount: 10}}, &Context{}); len(obs) != 0 {
		t.Errorf("nil Book: Observe returned %d observations, want 0", len(obs))
	}
}

// TestClusterEditionCountSignal_WeightAccessorIsZero pins the documented
// Weight() quirk: unlike every other signal in this package, this one has no
// single canonical magnitude (keep and exclude are independent), and Decide
// never reads Signal.Weight() for scoring — only the Weight field on each
// emitted FilterObservation. A regression here would mean someone added a
// meaning to the accessor this signal's doc explicitly says it doesn't have.
func TestClusterEditionCountSignal_WeightAccessorIsZero(t *testing.T) {
	s := NewClusterEditionCountSignal(300, -300, 3, 0)
	if w := s.Weight(); w != 0 {
		t.Errorf("Weight() = %v, want 0 (see the signal's doc: keepWeight/excludeWeight are independent, not one accessor value)", w)
	}
}
