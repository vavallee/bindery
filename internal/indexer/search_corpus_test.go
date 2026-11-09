package indexer

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// searchCorpusCase is one labelled release in testdata/search_corpus.json: the
// book the library wants, the release an indexer returned, and what a person
// would do with it. It is the measurement behind #3455, not a regression pin
// for one rule.
type searchCorpusCase struct {
	// Source names where the case comes from: an issue number, "clear" for a
	// plain true positive, "usenet" for fragments, "falsedrop" for a release a
	// person would grab that a filter might refuse.
	Source         string   `json:"source"`
	WantedTitle    string   `json:"wantedTitle"`
	WantedAuthor   string   `json:"wantedAuthor"`
	MediaType      string   `json:"mediaType"`
	SeriesTitle    string   `json:"seriesTitle"`
	SeriesPosition string   `json:"seriesPosition"`
	AuthorTitles   []string `json:"authorTitles"`
	Release        string   `json:"release"`
	Category       string   `json:"category"`
	// Expect is the correct answer, "keep" or "drop", not what the filters do.
	Expect string `json:"expect"`
	// KnownGap marks a case the filters currently get wrong. The test then
	// requires the filters to still get it wrong, so a fix shows up as a stale
	// gap that has to be removed, and a new mismatch cannot hide behind one.
	KnownGap  bool   `json:"knownGap"`
	GapReason string `json:"gapReason"`
	Note      string `json:"note"`
}

// runSearchCorpusCase sends one release through the same three filters, in the
// same order, as SearchBookWithDebug: usenet junk, non book content, relevance,
// then the pack check the automatic grab applies afterwards.
// It returns the stage that dropped the release, "" when it was kept, and the
// reason.
func runSearchCorpusCase(c searchCorpusCase) (stage, reason string) {
	r := newznab.SearchResult{GUID: "g", Title: c.Release, Category: c.Category}
	results := []newznab.SearchResult{r}

	results, junk := filterUsenetJunkDebug(results, junkScope{MediaType: c.MediaType, WantedTitle: c.WantedTitle})
	if len(junk) > 0 {
		return junk[0].Stage, junk[0].Reason
	}
	results, nonBook := filterNonBookContentDetailed(results)
	if len(nonBook) > 0 {
		return nonBook[0].Stage, nonBook[0].Reason
	}
	bc := bookContext{seriesPosition: c.SeriesPosition, seriesTitle: c.SeriesTitle, otherTitles: c.AuthorTitles}
	_, dropped := filterRelevantKeepDropped(results, c.WantedTitle, c.WantedAuthor, nil, bc)
	if len(dropped) > 0 {
		return dropped[0].Stage, dropped[0].Reason
	}
	// Not a search filter: decision.MultiBookPackSpec rejects an explicit pack
	// on the automatic grab path only, using this same marker. It is the stage
	// that stops the multi book releases the other three let through, so the
	// corpus runs it last and reports it under its own name.
	if m := MultiBookPackMarkerFor(c.Release, PackContext{Title: c.WantedTitle, SeriesTitle: c.SeriesTitle, AuthorTitles: c.AuthorTitles}); m != "" {
		return stagePackSpec, "multi-book pack marker " + m
	}
	return "", ""
}

// stagePackSpec names the pack check in the corpus table. It is not one of the
// Stage constants, which are the search filters' own.
const stagePackSpec = "pack-spec"

func loadSearchCorpus(t testing.TB) []searchCorpusCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/search_corpus.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var cases []searchCorpusCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	return cases
}

// TestSearchCorpus runs the labelled corpus through the filters and fails on
// any label mismatch that is not marked knownGap. It logs how many releases
// each stage dropped, split into right and wrong drops, and the totals.
func TestSearchCorpus(t *testing.T) {
	cases := loadSearchCorpus(t)
	if len(cases) < 60 {
		t.Fatalf("corpus has %d cases, want at least 60", len(cases))
	}

	type stageCount struct{ right, wrong int }
	stages := map[string]*stageCount{}
	for _, s := range []string{StageUsenetJunk, StageNonBook, StageRelevance, stagePackSpec} {
		stages[s] = &stageCount{}
	}
	var correct, falseDrops, junkPasses, keepWanted, dropWanted int
	var gaps []string

	for i, c := range cases {
		c := c
		label := fmt.Sprintf("#%d [%s] want %q by %s, release %q", i, c.Source, c.WantedTitle, c.WantedAuthor, c.Release)
		if c.Expect != "keep" && c.Expect != "drop" {
			t.Errorf("%s: expect must be keep or drop, got %q", label, c.Expect)
			continue
		}
		stage, reason := runSearchCorpusCase(c)
		kept := stage == ""
		if c.Expect == "keep" {
			keepWanted++
		} else {
			dropWanted++
		}
		if !kept {
			sc := stages[stage]
			if sc == nil {
				sc = &stageCount{}
				stages[stage] = sc
			}
			if c.Expect == "drop" {
				sc.right++
			} else {
				sc.wrong++
			}
		}

		mismatch := (c.Expect == "keep") != kept
		kind := ""
		switch {
		case mismatch && c.Expect == "keep":
			kind, falseDrops = "false drop", falseDrops+1
		case mismatch:
			kind, junkPasses = "junk passes", junkPasses+1
		default:
			correct++
		}

		switch {
		case mismatch && !c.KnownGap:
			t.Errorf("%s: expected %s, got %s (stage %q, reason %q). If this is a real gap in the filters, mark it knownGap with a gapReason", label, c.Expect, kindWord(kept), stage, reason)
		case !mismatch && c.KnownGap:
			t.Errorf("%s: marked knownGap but the filters now get it right; remove knownGap and gapReason", label)
		case mismatch && c.KnownGap:
			if strings.TrimSpace(c.GapReason) == "" {
				t.Errorf("%s: knownGap needs a gapReason", label)
			}
			gaps = append(gaps, fmt.Sprintf("%s | %s | %s (stage %q) | %s", kind, c.Source, c.Release, stage, c.GapReason))
		}
	}

	var b strings.Builder
	b.WriteString("\nper stage drops (stage, dropped, right, wrong)\n")
	names := make([]string, 0, len(stages))
	for s := range stages {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, s := range names {
		sc := stages[s]
		fmt.Fprintf(&b, "  %-12s %3d  right %3d  wrong %3d\n", s, sc.right+sc.wrong, sc.right, sc.wrong)
	}
	fmt.Fprintf(&b, "totals over %d releases (%d to keep, %d to drop)\n", len(cases), keepWanted, dropWanted)
	fmt.Fprintf(&b, "  correct          %3d\n  false drops      %3d\n  junk that passes %3d\n", correct, falseDrops, junkPasses)
	if len(gaps) > 0 {
		b.WriteString("known gaps\n")
		for _, g := range gaps {
			b.WriteString("  " + g + "\n")
		}
	}
	t.Log(b.String())
}

func kindWord(kept bool) string {
	if kept {
		return "kept"
	}
	return "dropped"
}

// BenchmarkScoreResult times scoreResult over the corpus releases, the work the
// ranker does for every release that survives the filters.
func BenchmarkScoreResult(b *testing.B) {
	cases := loadSearchCorpus(b)
	results := make([]newznab.SearchResult, len(cases))
	crit := make([]MatchCriteria, len(cases))
	for i, c := range cases {
		results[i] = newznab.SearchResult{GUID: fmt.Sprint(i), Title: c.Release, Size: 5 << 20, Grabs: 3}
		crit[i] = MatchCriteria{Title: c.WantedTitle, Author: c.WantedAuthor, MediaType: c.MediaType}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for i := range results {
			_ = scoreResult(results[i], crit[i], nil)
		}
	}
}
