package indexer

import "testing"

// #3952: a release that spells an accented title or author without the accent
// (very common for French, Spanish, Portuguese and Italian names) must pass
// the relevance filter, and so must the reverse.
func TestFilterRelevantAccentVariants(t *testing.T) {
	cases := []struct {
		name, title, author, release string
		keep                         bool
	}{
		{"issue example plain release", "Vallée du silicium", "Alain Damasio", "Alain.Damasio.Vallee.du.silicium.2024.French.Epub-noTag", true},
		{"issue example accented release", "Vallée du silicium", "Alain Damasio", "Alain.Damasio.Vallée.du.silicium.2024.French.Epub-noTag", true},
		{"plain wanted, accented release", "Vallee du silicium", "Alain Damasio", "Alain.Damasio.Vallée.du.silicium.2024.French.Epub-noTag", true},
		{"accented author, plain release", "Le Petit Prince", "Antoine de Saint-Exupéry", "Antoine de Saint-Exupery - Le Petit Prince (epub)", true},
		{"plain author, accented release", "Le Petit Prince", "Antoine de Saint-Exupery", "Antoine de Saint-Exupéry - Le Petit Prince (epub)", true},
		{"n tilde", "El señor de los anillos", "J.R.R. Tolkien", "J.R.R.Tolkien.El.senor.de.los.anillos.epub", true},
		{"c cedilla", "La façade", "Jean Dupont", "Jean.Dupont.La.facade.epub", true},
		{"o stroke author", "The Snowman", "Jo Nesbø", "Jo.Nesbo.-.The.Snowman.2007.RETAIL.EPUB-GRP", true},
		{"oe ligature", "Cœur de pierre", "Jean Dupont", "Jean.Dupont.Coeur.de.pierre.epub", true},
		{"eszett", "Die Straße", "Hans Meier", "Hans.Meier.Die.Strasse.epub", true},
		{"german umlaut digraph", "Die Höhle", "Hans Müller", "Hans.Mueller.Die.Hoehle.epub", true},
		{"german umlaut compact", "Die Höhle", "Hans Müller", "Hans.Muller.Die.Hohle.epub", true},
		{"cyrillic unchanged", "Война и мир", "Лев Толстой", "Лев.Толстой.Война.и.мир.epub", true},
		{"different title stays dropped", "Vallée du silicium", "Alain Damasio", "Alain.Damasio.La.Horde.du.Contrevent.epub", false},
		{"different author stays dropped", "Vallée du silicium", "Alain Damasio", "Marie.Curie.Vallee.du.silicium.epub", false},
		{"circumflex", "Pêche", "Jean Dupont", "Jean.Dupont.Peche.et.Cie.epub", true},
		{"unrelated word stays dropped", "Vallée", "Jean Dupont", "Jean.Dupont.Valle.epub", false},
	}
	for _, tc := range cases {
		got := filterRelevant(toResults(tc.release), tc.title, tc.author, nil)
		if (len(got) == 1) != tc.keep {
			t.Errorf("%s: %q by %q vs %q kept=%v, want %v", tc.name, tc.title, tc.author, tc.release, len(got) == 1, tc.keep)
		}
	}
}
