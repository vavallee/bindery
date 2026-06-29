package db

import "testing"

func TestAuthorSortKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"Adams, Douglas", "adams, douglas"},
		{"adelson, Anita", "adelson, anita"},       // case folds
		{"de Balzac, Honoré", "de balzac, honore"}, // accent in tail folds
		{"Zola, Émile", "zola, emile"},
		{"Östergaard, Karl", "ostergaard, karl"}, // leading diacritic → base letter
		{"Ángel, José", "angel, jose"},
		{"Çelik, Ayşe", "celik, ayse"},
		{"Nowak, Łukasz", "nowak, lukasz"}, // ł is non-decomposable
		{"Ørsted, Hans", "orsted, hans"},   // ø is non-decomposable
		{"Æsop", "aesop"},                  // ligature expands
		{"Straße, Anna", "strasse, anna"},  // ß → ss
	}
	for _, c := range cases {
		if got := authorSortKey(c.in); got != c.want {
			t.Errorf("authorSortKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Leading-diacritic keys must order by their folded base letter, not after "z".
func TestAuthorSortKey_Ordering(t *testing.T) {
	// Östergaard folds to 'o', so it must sort between "nowak" and "zola".
	o := authorSortKey("Östergaard, Karl")
	if !(authorSortKey("Nowak, Łukasz") < o && o < authorSortKey("Zola, Émile")) {
		t.Errorf("Östergaard key %q not ordered between Nowak and Zola", o)
	}
}
