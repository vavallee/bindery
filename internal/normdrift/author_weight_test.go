package normdrift

import (
	"testing"

	"github.com/vavallee/bindery/internal/textutil"
)

// TestAuthorMatchWeightIsSymmetric extends the existing Kind symmetry property
// to the number Kind is now derived from. Callers pass the incoming name first
// at some sites and the stored name first at others, and internal/abs ranks
// candidates on the weight, so an asymmetric weight would pick a different
// author depending on which side of the comparison a row happened to be on.
func TestAuthorMatchWeightIsSymmetric(t *testing.T) {
	corpus := append([]string{}, adversarialAuthors...)
	for _, forename := range longForenames {
		for _, surname := range shortSurnames {
			corpus = append(corpus, forename+" "+surname)
		}
	}
	corpus = append(corpus, "Stanley Paul", "Paul Stanley", "Liu Cixin", "Nick Lane", "N. Lane", "Tolkien")
	for _, a := range corpus {
		for _, b := range corpus {
			ab := textutil.MatchAuthorName(a, b)
			ba := textutil.MatchAuthorName(b, a)
			if ab.Kind != ba.Kind || ab.Weight != ba.Weight {
				t.Errorf("MatchAuthorName is asymmetric for %q / %q: %v w=%.1f vs %v w=%.1f",
					a, b, ab.Kind, ab.Weight, ba.Kind, ba.Weight)
			}
		}
	}
}

// TestAuthorMatchBandsAreOrdered pins the relationship between the two exported
// weight thresholds and the bands, so that a later edit cannot invert them or
// close the ambiguous band without saying so here.
func TestAuthorMatchBandsAreOrdered(t *testing.T) {
	if textutil.AuthorMatchAmbiguousWeight >= textutil.AuthorMatchAutoWeight {
		t.Fatalf("ambiguous weight %.1f must be below the auto weight %.1f",
			textutil.AuthorMatchAmbiguousWeight, textutil.AuthorMatchAutoWeight)
	}
	// Every band must be reachable, or the callers that branch on three
	// outcomes are really branching on two.
	seen := map[textutil.AuthorMatchKind]bool{}
	for _, pair := range [][2]string{
		{"J. R. R. Tolkien", "J.R.R. Tolkien"},
		{"J.R.R. Tolkien", "John Ronald Reuel Tolkien"},
		{"Alice Jones", "Alice James"},
		{"Jane Doe", "Neal Stephenson"},
	} {
		seen[textutil.MatchAuthorName(pair[0], pair[1]).Kind] = true
	}
	for _, kind := range []textutil.AuthorMatchKind{
		textutil.AuthorMatchNone,
		textutil.AuthorMatchExact,
		textutil.AuthorMatchFuzzyAuto,
		textutil.AuthorMatchFuzzyAmbiguous,
	} {
		if !seen[kind] {
			t.Errorf("band %v is unreachable from the sample pairs", kind)
		}
	}
}
