package decision_test

import (
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/models"
)

// freeleechRelease builds a torrent release on indexer 7 with the given
// downloadvolumefactor. Pass nil for "the indexer did not report it".
func freeleechRelease(dvf *float64) decision.Release {
	r := release(withProtocol("torrent"))
	r.IndexerID = 7
	r.DownloadVolumeFactor = dvf
	return r
}

func dvf(v float64) *float64 { return &v }

func flaggedSpec() decision.FreeleechOnlySpec {
	return decision.FreeleechOnlySpec{IndexerIDs: map[int64]bool{7: true}}
}

// A freeleech release (downloadvolumefactor 0) costs no ratio and must pass.
func TestFreeleechOnlySpec_FreeleechPasses(t *testing.T) {
	ok, reason := flaggedSpec().IsSatisfiedBy(freeleechRelease(dvf(0)), emptyBook())
	if !ok {
		t.Fatalf("downloadvolumefactor 0 is freeleech and must pass, got rejection %q", reason)
	}
}

// A normal release costs full ratio and must be held.
func TestFreeleechOnlySpec_NonFreeleechHeld(t *testing.T) {
	ok, reason := flaggedSpec().IsSatisfiedBy(freeleechRelease(dvf(1)), emptyBook())
	if ok {
		t.Fatal("downloadvolumefactor 1 costs ratio and must be held")
	}
	if !strings.Contains(reason, decision.RejectionFreeleechHold) {
		t.Errorf("rejection %q must contain the %q sentinel — the scheduler matches on it to park the release in pending_releases",
			reason, decision.RejectionFreeleechHold)
	}
}

// Half-leech still costs ratio, so it is held too.
func TestFreeleechOnlySpec_HalfLeechHeld(t *testing.T) {
	ok, _ := flaggedSpec().IsSatisfiedBy(freeleechRelease(dvf(0.5)), emptyBook())
	if ok {
		t.Fatal("downloadvolumefactor 0.5 still costs ratio and must be held")
	}
}

// Fail closed: an unreported factor must be held, not assumed free. Assuming
// free would silently spend the ratio this policy exists to protect.
func TestFreeleechOnlySpec_UnknownFactorHeld(t *testing.T) {
	ok, reason := flaggedSpec().IsSatisfiedBy(freeleechRelease(nil), emptyBook())
	if ok {
		t.Fatal("an unreported downloadvolumefactor must fail closed, not be assumed freeleech")
	}
	if !strings.Contains(reason, decision.RejectionFreeleechHold) {
		t.Errorf("rejection %q must carry the hold sentinel so the release is parked, not discarded", reason)
	}
}

// Indexers without the policy are untouched, whatever their ratio cost.
func TestFreeleechOnlySpec_OtherIndexerUnaffected(t *testing.T) {
	r := freeleechRelease(dvf(1))
	r.IndexerID = 99 // not in the flagged set
	if ok, reason := flaggedSpec().IsSatisfiedBy(r, emptyBook()); !ok {
		t.Fatalf("release from an unflagged indexer must pass, got %q", reason)
	}
}

// An empty policy set is a no-op, so the common (no private tracker) case is
// never affected.
func TestFreeleechOnlySpec_EmptySetIsNoOp(t *testing.T) {
	var s decision.FreeleechOnlySpec
	if ok, reason := s.IsSatisfiedBy(freeleechRelease(dvf(1)), emptyBook()); !ok {
		t.Fatalf("empty policy set must pass everything, got %q", reason)
	}
}

// Usenet has no ratio economy and never reports the factor; holding those
// would be pure noise if the policy were enabled on a newznab indexer.
func TestFreeleechOnlySpec_UsenetPasses(t *testing.T) {
	r := freeleechRelease(nil)
	r.Protocol = "usenet"
	if ok, reason := flaggedSpec().IsSatisfiedBy(r, emptyBook()); !ok {
		t.Fatalf("usenet release must pass (no ratio economy), got %q", reason)
	}
}

// The spec must reject through a full DecisionMaker too — that is the path the
// scheduler uses, including when re-evaluating pending releases (a held
// release has to keep failing until the user approves it by hand).
func TestFreeleechOnlySpec_RejectsViaDecisionMaker(t *testing.T) {
	dm := decision.New(flaggedSpec())
	decisions := dm.Evaluate([]decision.Release{freeleechRelease(dvf(1))}, models.Book{})
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Approved {
		t.Error("a ratio-costing release must stay rejected on re-evaluation, otherwise the scheduler auto-grabs it off the pending queue")
	}
}
