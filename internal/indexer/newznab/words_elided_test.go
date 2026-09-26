package newznab

import "testing"

// SigWordsElided is the separated reading of a title. SigWords keeps its own
// behaviour, so nothing that relies on the possessive form changes.
func TestSigWordsElided(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// Elision: the apostrophe joins a clitic to the next word, so the
		// separated reading must emit that word on its own.
		{"L'Outsider", []string{"outsider"}},
		{"L\u2019Outsider", []string{"outsider"}},
		{"L'Institut", []string{"institut"}},
		// "os" is under SigWords' three-byte floor, so it is dropped here for
		// the same reason "s" is in the possessive below.
		{"Sac d'os", []string{"sac"}},
		{"Ender's Game", []string{"ender", "game"}},
		// No apostrophe: no fallback to offer.
		{"Dune", nil},
	}
	for _, tc := range cases {
		got := SigWordsElided(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("SigWordsElided(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("SigWordsElided(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}

	// The strict form is untouched: possessives still collapse to one token,
	// which is the whole reason the deleted form exists.
	if got := SigWords("Ender's Game"); len(got) != 2 || got[0] != "enders" {
		t.Fatalf(`SigWords("Ender's Game") = %v, want [enders game]`, got)
	}
	if got := SigWords("L'Outsider"); len(got) != 1 || got[0] != "loutsider" {
		t.Fatalf(`SigWords("L'Outsider") = %v, want [loutsider]`, got)
	}
}

// The query-side gate, reached BEFORE any filtering exists: it decides whether
// an indexer's response is worth keeping at all. Its fold deliberately does not
// split on punctuation (see foldForSigWordMatch), so the strict token of an
// elided title is absent from a dotted release name and the entire response was
// discarded — which is why correcting the filters alone changed nothing.
func TestTitleHasRelevantResultElided(t *testing.T) {
	dotted := []SearchResult{{Title: "Stephen.King.L.Outsider.2018.FRENCH.[ePub]-NOTAG"}}

	if !titleHasRelevantResult("L'Outsider", dotted) {
		t.Fatal("elided query title rejected the response carrying its own release")
	}
	if !titleHasRelevantResult("L\u2019Outsider", dotted) {
		t.Fatal("typographic apostrophe: elided query title rejected its own release")
	}

	// No apostrophe means no fallback, so an unrelated response is still
	// rejected: the second pass must not become a way to accept junk.
	unrelated := []SearchResult{{Title: "Stephen.King.The.Stand.1978.FRENCH.ePub-NOTAG"}}
	if titleHasRelevantResult("L'Outsider", unrelated) {
		t.Fatal("elided fallback accepted an unrelated response")
	}
	if titleHasRelevantResult("Dune", unrelated) {
		t.Fatal("title without an apostrophe accepted an unrelated response")
	}
}
