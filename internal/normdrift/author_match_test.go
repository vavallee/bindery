package normdrift

import (
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/textutil"
)

// shortSurnames are five runes or fewer and pairwise different people. Each
// pair shares a first letter, which is what a Jaro-Winkler prefix bonus
// rewards most and what a short string has least room to contradict.
var shortSurnames = []string{"Jones", "James", "Ross", "Rose", "Kelly", "Kelsy", "Rice", "Rich", "Hall", "Hale", "Lane", "Lang", "Bond", "Bonn"}

// oldWholeNameAutoThreshold is the whole-name Jaro-Winkler auto-accept the
// additive scorer replaced. It is written out here rather than read from
// textutil, whose copy is deprecated and goes away next release: what this
// test needs is the historical number the corpus below is measured against,
// and that number stays 0.94 whatever happens to the constant.
const oldWholeNameAutoThreshold = 0.94

// longForenames are the other half of the trap: the longer the shared
// forename, the more of the whole-name score it accounts for, so a pair that
// disagrees only on a short surname scores higher the more of the name the two
// people have in common.
var longForenames = []string{"Christopher", "Alexander", "Jonathan", "Elizabeth"}

// TestShortSurnamesNeverAutoMatchOnJWAlone is the property the additive scorer
// exists to provide, and the one a single Jaro-Winkler gate cannot.
//
// Two people who share a forename and whose surnames merely start alike score
// higher, on the whole name, the more of the name they share:
// "Christopher Ross" against "Christopher Rose" reaches 0.9750, over any auto
// threshold this codebase has ever used. Nothing here may auto-match. The
// second half of the test is the part that matters — it asserts that some of
// these pairs really do clear the old threshold, so the property is about the
// decision rule and not about a corpus that happens to score low.
func TestShortSurnamesNeverAutoMatchOnJWAlone(t *testing.T) {
	clearedOldThreshold := 0
	for _, forename := range longForenames {
		for i, left := range shortSurnames {
			for _, right := range shortSurnames[i+1:] {
				a := forename + " " + left
				b := forename + " " + right
				got := textutil.MatchAuthorName(a, b)
				if got.Kind == textutil.AuthorMatchExact || got.Kind == textutil.AuthorMatchFuzzyAuto {
					t.Errorf("MatchAuthorName(%q, %q) = %v (jw %.4f), want no auto-match",
						a, b, got.Kind, got.Score)
				}
				if got.Score >= oldWholeNameAutoThreshold {
					clearedOldThreshold++
				}
			}
		}
	}
	if clearedOldThreshold == 0 {
		t.Fatal("no pair cleared the whole-name auto threshold: this property is no longer testing the decision rule")
	}
	t.Logf("%d pairs of different people cleared the whole-name Jaro-Winkler auto threshold and were refused on field evidence", clearedOldThreshold)
}

// TestShortSurnamesStillMatchThemselves is the other side of the same rule. A
// short surname is required to be EQUAL, not similar — so equality, in any of
// the spellings the variant chain produces, must still match.
func TestShortSurnamesStillMatchThemselves(t *testing.T) {
	for _, forename := range longForenames {
		for _, surname := range shortSurnames {
			a := forename + " " + surname
			for _, b := range []string{
				a,
				strings.ToUpper(a),
				surname + ", " + forename,
				string([]rune(forename)[:1]) + ". " + surname,
			} {
				if got := textutil.MatchAuthorName(a, b); got.Kind != textutil.AuthorMatchExact && got.Kind != textutil.AuthorMatchFuzzyAuto {
					t.Errorf("MatchAuthorName(%q, %q) = %v, want a match", a, b, got.Kind)
				}
			}
		}
	}
}
