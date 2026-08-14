package textutil

import "testing"

func TestLooksLikeCollectionTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"The Complete Asimov Stories", true},
		{"Collected Poems", true},
		{"Dune Omnibus", true},
		{"Box Set: Stormlight Archive", true},
		{"The Best of Philip K. Dick", true},
		{"Dune", false},
		{"Complete Guide to Go Programming", true}, // false positive we accept
		{"The Collected Works of Terry Pratchett, Vol. 3", true},
		// #1780's real-world OpenLibrary examples for J.R.R. Tolkien.
		{"The Complete History of Middle-Earth", true},
		{"The Lord of the Rings 3 Books Box Set By J. R. R. Tolkien", true},
		{"Lord of the Rings Deluxe Illustrated Box Set", true},
		{"Lord of the Rings Collector's Edition Box Set", true},
	}
	for _, c := range cases {
		if got := LooksLikeCollectionTitle(c.title); got != c.want {
			t.Errorf("LooksLikeCollectionTitle(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}
