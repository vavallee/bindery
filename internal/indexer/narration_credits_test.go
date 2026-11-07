package indexer

import (
	"reflect"
	"testing"
)

func TestParseNarration_TrailingWordsAndCredits(t *testing.T) {
	herbert := []string{"Frank Herbert"}
	cases := []struct {
		name, title, book string
		authors           []string
		want              []string
	}{
		{"trailing Audible", "Dune read by Scott Brick Audible", "Dune", nil, []string{"Scott Brick"}},
		{"trailing Retail", "Dune read by Scott Brick Retail", "Dune", nil, []string{"Scott Brick"}},
		{"trailing English epub", "Dune read by Scott Brick English epub", "Dune", nil, []string{"Scott Brick"}},
		{"trailing year and format", "Dune read by Scott Brick 2007 mp3", "Dune", nil, []string{"Scott Brick"}},
		{"publisher after comma", "Dune read by Scott Brick, Audible Studios", "Dune", nil, []string{"Scott Brick"}},
		{"other role after narrator", "Dune by Frank Herbert, narrated by Scott Brick, translated by Jean Dupont", "Dune", herbert, []string{"Scott Brick"}},
		{"with joins narrators", "Dune narrated by Scott Brick with Simon Vance", "Dune", nil, []string{"Scott Brick", "Simon Vance"}},
		{"dash wrapped edition", "Dune read by Scott Brick -Unabridged-", "Dune", nil, []string{"Scott Brick"}},
		{"stated author narrator", "Dune - Read by Frank Herbert [Unabridged]", "Dune", herbert, []string{"Frank Herbert"}},
		// Negative controls.
		{"read by the author is a role, unknown", "Dune read by the author Frank Herbert", "Dune", herbert, []string{}},
		{"only a publisher after read by", "Dune read by Audible Studios", "Dune", nil, []string{}},
		{"author listed beside narrator tag", "Frank Herbert; Scott Brick (narrator) - Dune", "Dune", herbert, []string{"Scott Brick"}},
		{"author in a comma list before tag", "Frank Herbert, Scott Brick (narrator) - Dune", "Dune", herbert, []string{"Scott Brick"}},
		{"book title containing read by", "Why I Read By Candlelight Jane Doe", "Why I Read By Candlelight", nil, []string{}},
		{"book title containing narrated by", "Narrated By Night - Jane Doe m4b", "Narrated By Night", nil, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseNarration(tc.title, []string{tc.book}, tc.authors).Narrators
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseNarration(%q) narrators = %#v, want %#v", tc.title, got, tc.want)
			}
		})
	}
}
