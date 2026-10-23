package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestTrailingTitleParsePosition(t *testing.T) {
	cases := []struct {
		in     string
		lo, hi int
		ok     bool
	}{
		{"3", 3, 3, true}, {"03", 3, 3, true}, {" 2.0 ", 2, 2, true}, {"1-3", 1, 3, true},
		{"1 - 3", 1, 3, true}, {"3-1", 0, 0, false}, {"2.5", 0, 0, false}, {"", 0, 0, false},
		{"Prequel", 0, 0, false}, {"0", 0, 0, true},
	}
	for _, c := range cases {
		lo, hi, ok := parsePosition(c.in)
		if lo != c.lo || hi != c.hi || ok != c.ok {
			t.Errorf("parsePosition(%q) = %d,%d,%v want %d,%d,%v", c.in, lo, hi, ok, c.lo, c.hi, c.ok)
		}
	}
}

func TestTrailingTitleTokenizeKeepsSeparators(t *testing.T) {
	toks, ok := tokenizeRelease("Leigh_Bardugo_-_King_of_Scars_02-Rule.of.Wolves (Müller's)")
	if !ok {
		t.Fatal("tokenisation disagreed with NormalizeRelease")
	}
	var got []string
	for _, tk := range toks {
		got = append(got, tk.text)
	}
	want := "leigh bardugo king of scars 02 rule of wolves muellers"
	if strings.Join(got, " ") != want {
		t.Fatalf("tokens = %q, want %q", strings.Join(got, " "), want)
	}
	if !punctBefore(toks[2]) {
		t.Errorf("dash before king not seen, sep=%q", toks[2].sep)
	}
	if !punctBefore(toks[6]) {
		t.Errorf("a hyphen after the number is punctuation, sep=%q", toks[6].sep)
	}
	if punctBefore(toks[7]) {
		t.Errorf("a dot is a space in a release name, sep=%q", toks[7].sep)
	}
}

// TestTrailingTitleQuietCases pins the reasons signal B and the position rule
// stay quiet.
func TestTrailingTitleQuietCases(t *testing.T) {
	// A library title that contains the wanted one is not "another title".
	kept, reason := relevanceVerdict("Rotten School", "R. L. Stine",
		"R L Stine - Rotten School - Rotten School Gets Dirty (retail) (epub)",
		bookContext{otherTitles: []string{"Rotten School Gets Dirty"}})
	if !kept {
		t.Errorf("a library title containing the wanted title must not reject: %s", reason)
	}
	// Single word library titles never count.
	kept, reason = relevanceVerdict("King of Scars", "Leigh Bardugo",
		"Leigh Bardugo - King of Scars - Wolves (retail) (epub)",
		bookContext{otherTitles: []string{"Wolves"}})
	if !kept {
		t.Errorf("a one word library title must not reject: %s", reason)
	}
	// A wanted position range keeps the numbers inside it.
	kept, reason = relevanceVerdict("Mistborn", "Brandon Sanderson",
		"Brandon Sanderson - Mistborn 02 - The Well of Ascension (retail) (epub)",
		bookContext{seriesPosition: "1-3"})
	if !kept {
		t.Errorf("a number inside the wanted range must not reject: %s", reason)
	}
	kept, _ = relevanceVerdict("Mistborn", "Brandon Sanderson",
		"Brandon Sanderson - Mistborn 04 - The Alloy of Law (retail) (epub)",
		bookContext{seriesPosition: "1-3"})
	if kept {
		t.Errorf("a number outside the wanted range must reject")
	}
}

// TestTrailingTitleReachesEveryEntrypoint runs the three production entry
// points against one fake indexer, as
// TestRelevanceGuardsReachEveryProductionEntrypoint does, with the library
// context carried on MatchCriteria.
func TestTrailingTitleReachesEveryEntrypoint(t *testing.T) {
	right := "Leigh Bardugo - King of Scars (Unabridged) MP3"
	wrong := "Leigh Bardugo - King of Scars 02 - Rule of Wolves.par2"
	wrongKnown := "Leigh Bardugo - King of Scars - Rule of Wolves (retail) (epub)"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(feedOf(right, wrong, wrongKnown)))
	}))
	defer srv.Close()
	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{3030}}}
	crit := MatchCriteria{
		Title: "King of Scars", Author: "Leigh Bardugo", MediaType: models.MediaTypeAudiobook,
		SeriesPosition: "1", AuthorTitles: []string{"Rule of Wolves"},
	}
	ctx := context.Background()
	plain := newTestSearcher().SearchBook(ctx, idxs, crit)
	debugged, dbg := newTestSearcher().SearchBookWithDebug(ctx, idxs, crit)
	outcomes, _ := newTestSearcher().SearchBookWithOutcomes(ctx, idxs, crit)
	for name, got := range map[string][]string{
		"SearchBook":             resultTitles(plain),
		"SearchBookWithDebug":    resultTitles(debugged),
		"SearchBookWithOutcomes": resultTitles(outcomes),
	} {
		if len(got) != 1 || got[0] != right {
			t.Errorf("%s kept %v, want only %q", name, got, right)
		}
	}
	reasons := map[string]string{}
	for _, f := range dbg.Filters {
		reasons[f.Title] = f.Reason
	}
	if reasons[wrong] != reasonLaterNumber {
		t.Errorf("debug reason for %q = %q, want %q", wrong, reasons[wrong], reasonLaterNumber)
	}
	if reasons[wrongKnown] != reasonAnotherTitle {
		t.Errorf("debug reason for %q = %q, want %q", wrongKnown, reasons[wrongKnown], reasonAnotherTitle)
	}
}
