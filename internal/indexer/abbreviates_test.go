package indexer

import "testing"

// TestAbbreviatesSeriesWords pins the run together abbreviation the run on
// rule accepts: the leading words of the series or subtitle, cut short and
// joined, four letters or more.
func TestAbbreviatesSeriesWords(t *testing.T) {
	seq := []string{"expeditionary", "force"}
	for tok, want := range map[string]bool{
		"exforce": true, "exfor": true, "expforce": true,
		"exf": false, "force": false, "forc": false, "ride": false, "expedition": false,
	} {
		if got := abbreviates(tok, seq); got != want {
			t.Errorf("abbreviates(%q, %v) = %v, want %v", tok, seq, got, want)
		}
	}
}
