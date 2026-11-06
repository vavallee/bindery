package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

func rejectRunFor(title, author, words string, titles ...string) (kept []string, dropped []DroppedRelease) {
	var in []newznab.SearchResult
	for i, t := range titles {
		in = append(in, newznab.SearchResult{GUID: string(rune('a' + i)), Title: t, NZBURL: "https://fake/" + string(rune('a'+i))})
	}
	k, d := filterRelevantKeepDropped(in, title, author, nil,
		MatchCriteria{RejectWords: ParseRejectWords(words)}.context())
	for _, r := range k {
		kept = append(kept, r.Title)
	}
	return kept, d
}

// Accents fold on both sides, in both directions.
func TestRejectWords_AccentsFoldBothWays(t *testing.T) {
	cases := []struct{ words, title string }{
		{"Dramatise", "Frank Herbert - Dune DRAMATISÉ"},
		{"Dramatisé", "Frank Herbert - Dune dramatise"},
		{"Édition Graphique", "Frank Herbert - Dune Edition.Graphique"},
		{"Edition Graphique", "Frank Herbert - Dune Édition Graphique"},
		{"Dramatisé", "Frank Herbert - Dune DRAMATISÉ"},
	}
	for _, c := range cases {
		if _, dropped := rejectRun(c.words, c.title); len(dropped) != 1 {
			t.Errorf("entry %q did not reject %q", c.words, c.title)
		}
	}
}

// A rejected word that is part of the wanted book's own title or author does
// not empty the search.
func TestRejectWords_OwnTitleAndAuthorExempt(t *testing.T) {
	kept, dropped := rejectRunFor("Cast Away", "Tom Hanks", "Cast, Full Cast",
		"Tom Hanks - Cast Away mp3",
		"Tom Hanks - Cast Away Full Cast mp3")
	if len(kept) != 1 || kept[0] != "Tom Hanks - Cast Away mp3" {
		t.Fatalf("kept = %v", kept)
	}
	if len(dropped) != 1 || dropped[0].Result.GUID != "b" {
		t.Fatalf("dropped = %+v", dropped)
	}

	kept, dropped = rejectRunFor("Dune", "Frank Herbert", "Frank",
		"Frank Herbert - Dune epub", "Herbert, Frank - Dune mobi")
	if len(kept) != 2 || len(dropped) != 0 {
		t.Fatalf("author word dropped releases: kept=%v dropped=%+v", kept, dropped)
	}

	// A phrase not wholly inside the title or author still applies.
	_, dropped = rejectRunFor("Cast Away", "Tom Hanks", "Away Full",
		"Tom Hanks - Cast Away Full Cast mp3")
	if len(dropped) != 1 {
		t.Fatalf("a phrase that is not wholly the book's own was exempted")
	}
	// Accent differences do not defeat the exemption.
	kept, _ = rejectRunFor("Édition", "Anon", "Edition", "Anon - Édition mp3")
	if len(kept) != 1 {
		t.Fatalf("accented own title was rejected")
	}
}

// Numbers and symbols behave as tokens; CJK entries match whole runs.
func TestRejectWords_TokenEdges(t *testing.T) {
	if _, d := rejectRun("2", "Frank Herbert - Dune 12"); len(d) != 0 {
		t.Error("2 hit 12")
	}
	if _, d := rejectRun("C++", "Frank Herbert - Dune C Programming"); len(d) != 1 {
		t.Error("C++ should behave as the token c")
	}
	// A semicolon is not a separator: the entry is one phrase, "abridged
	// dramatized", so neither word alone rejects anything.
	if _, d := rejectRun("abridged;dramatized", "Frank Herbert - Dune abridged", "Frank Herbert - Dune dramatized"); len(d) != 0 {
		t.Error("a semicolon must not separate entries")
	}
	if _, d := rejectRun("完全版", "Frank Herbert - Dune 完全版 epub"); len(d) != 1 {
		t.Error("CJK run did not match")
	}
	if _, d := rejectRun("完全", "Frank Herbert - Dune 完全版 epub"); len(d) != 0 {
		t.Error("CJK substring matched inside a longer run")
	}
}
