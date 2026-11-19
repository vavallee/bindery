package textutil

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// #3952: Latin accents fold on both sides of a title comparison, so "Vallée"
// and "Vallee" reach one spelling, while other scripts are left alone.
func TestFoldForTitleMatchLatinAccents(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"issue example", "Vallée du silicium", "vallee du silicium"},
		{"already plain", "Vallee du silicium", "vallee du silicium"},
		{"grave", "Où", "ou"},
		{"n tilde", "El Señor", "el senor"},
		{"c cedilla", "Façade", "facade"},
		{"o stroke", "Jo Nesbø", "jo nesbo"},
		{"a ring", "Åsa", "asa"},
		{"ae ligature", "Cæsar", "caesar"},
		{"oe ligature", "Cœur", "coeur"},
		{"l stroke", "Łukasz", "lukasz"},
		{"eszett", "Straße", "strasse"},
		{"german umlaut still expands", "Müller", "mueller"},
		{"decomposed accent", norm.NFD.String("Vallée"), "vallee"},
		{"cyrillic unchanged", "Война и мир Й", "война и мир й"},
		{"cyrillic short i keeps its breve", "й", "й"},
		{"greek unchanged", "Ἰλιάς", "ἰλιάς"},
		{"cjk unchanged", "三体", "三体"},
		{"kana dakuten unchanged", "ハード", "ハード"},
	}
	for _, tc := range cases {
		if got := FoldForTitleMatch(tc.in); got != tc.want {
			t.Errorf("%s: FoldForTitleMatch(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// The keyword side keeps ä, ö and ü (so the umlaut flex can tell them from a
// typed digraph, #3123) but folds every other Latin accent.
func TestFoldForKeywordLatinAccents(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Vallée", "vallee"},
		{"Müller", "müller"},
		{"Nesbø", "nesbo"},
		{"Œuvres", "oeuvres"},
		{"Straße", "strasse"},
		{"Война", "война"},
		{norm.NFD.String("Müller Vallée"), "müller vallee"},
	}
	for _, tc := range cases {
		if got := FoldForKeyword(tc.in); got != tc.want {
			t.Errorf("FoldForKeyword(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
