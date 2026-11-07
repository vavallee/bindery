package indexer

import "testing"

// TestAbbreviatesSeriesWords pins the run together abbreviation the run on
// rule accepts: the leading words of the series or subtitle, cut short and
// joined, five letters or more. A short ordinary word that happens to split
// into word prefixes is not an abbreviation.
func TestAbbreviatesSeriesWords(t *testing.T) {
	cases := []struct {
		tok  string
		seq  []string
		want bool
	}{
		{"exforce", []string{"expeditionary", "force"}, true},
		{"exfor", []string{"expeditionary", "force"}, true},
		{"expforce", []string{"expeditionary", "force"}, true},
		{"firenoble", []string{"fireteam", "noble"}, true},
		{"exf", []string{"expeditionary", "force"}, false},
		{"force", []string{"expeditionary", "force"}, false},
		{"forc", []string{"expeditionary", "force"}, false},
		{"ride", []string{"expeditionary", "force"}, false},
		{"expedition", []string{"expeditionary", "force"}, false},
		{"risk", []string{"rise", "skywalker"}, false},
		{"most", []string{"morning", "star"}, false},
		{"fino", []string{"fireteam", "noble"}, false},
	}
	for _, c := range cases {
		if got := abbreviates(c.tok, c.seq); got != c.want {
			t.Errorf("abbreviates(%q, %v) = %v, want %v", c.tok, c.seq, got, c.want)
		}
	}
}
