package indexer

import (
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

func TestGluedLanguageCodes(t *testing.T) {
	cases := []struct {
		title string
		want  []string
	}{
		// The reported name and the issue's examples.
		{"Dungeon Crawler Carl_EspañolNeutro m4b", []string{"spa"}},
		{"Dungeon Crawler Carl EspanolLatino m4b", []string{"spa"}},
		{"Dungeon Crawler Carl DeutschUngekürzt", []string{"ger"}},
		{"Dungeon Crawler Carl ItalianoAudiolibro", []string{"ita"}},
		// Name at the end, both orders, acronym style capitals.
		{"Dungeon Crawler Carl AudiobookFrench", []string{"fre"}},
		{"Carl.ITALIANOAudiolibro.m4b", []string{"ita"}},
		{"Carl-AudiolibroItaliano", []string{"ita"}},
		{"FrançaisAudio", []string{"fre"}},
		{"AudioFrançais", []string{"fre"}},
		{"PortuguêsAudio", []string{"por"}},
		{"Carl SvenskaLjudbok", []string{"swe"}},
		// Not glued by capitals, or not a name: no change in behaviour.
		{"Finishing School", nil},
		{"PolishedStone", nil},
		{"Spanishmoss", nil},
		{"Danishes and Other Treats", nil},
		{"Germane Greer", nil},
		{"The Spanish Princess", nil},
		{"Turkey Dinner", nil},
		{"Italics Matter", nil},
		{"Chinese Whispers", nil},
		{"NailPolish Chronicles", nil},
		{"ItsDeepNoDe Es Ita", nil},
		{"Carl_DENeutro", nil},
		{"CarlEsNeutro", nil},
		{"ItalicsDutchless", nil},
		{"FinishLine", nil},
		{"NeutroEspanolLatino", nil},    // a name in the middle is not at a token edge
		{"HindiRocks", []string{"hin"}}, // honest edge: a capitalised name at a token edge counts
	}
	for _, c := range cases {
		got := gluedLanguageCodes(c.title)
		if !slices.Equal(got, c.want) {
			t.Errorf("gluedLanguageCodes(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}

func TestFilterByAllowedLanguagesGluedName(t *testing.T) {
	in := []newznab.SearchResult{
		{Title: "Dungeon Crawler Carl_EspañolNeutro m4b"},
		{Title: "Dungeon Crawler Carl m4b"},
		{Title: "Dungeon Crawler Carl EnglishAudiobook"},
		{Title: "Finishing School PolishedStone"},
	}
	kept, dropped := SplitByAllowedLanguages(in, []string{"eng"})
	if len(dropped) != 1 || dropped[0].Title != in[0].Title || len(kept) != 3 {
		t.Fatalf("kept %v dropped %v", kept, dropped)
	}
	kept, _ = SplitByAllowedLanguages(in, []string{"spa", "eng"})
	if len(kept) != 4 {
		t.Fatalf("spa+eng kept %d", len(kept))
	}
}
