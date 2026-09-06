package textutil

import "testing"

// TestSortNameHandlesParticles is the LC-PCC MGD "Access Point for Person"
// policy expressed as cases. Each row is a rule, not an example, so a change
// that breaks one is a change to the cataloguing policy and not a tidy-up.
//
// The policy is language-specific and this function does not know the
// language, so it uses the tables plus the BibTeX case rule. The rows below
// are the ones that pin that: "van Gogh" trails on a lowercase particle,
// "De Quincey" leads on a capitalised one, "de la Cruz" trails as a compound
// because the first token of the run decides for the run, and "Du Maurier"
// leads because French files it that way whatever the case.
func TestSortNameHandlesParticles(t *testing.T) {
	cases := []struct{ in, want, rule string }{
		{"Johann Wolfgang von Goethe", "Goethe, Johann Wolfgang von", "German von trails"},
		{"Vincent van Gogh", "Gogh, Vincent van", "Dutch van trails when lowercase"},
		{"Ludwig van Beethoven", "Beethoven, Ludwig van", "same, the most-cited example"},
		{"Vincent van der Berg", "Berg, Vincent van der", "multi-token particle run trails whole"},
		{"Charles de Gaulle", "Gaulle, Charles de", "French de trails"},
		{"Leonardo da Vinci", "Vinci, Leonardo da", "Italian da trails"},
		{"Jose de la Cruz", "Cruz, Jose de la", "Spanish de la is a compound and trails as one"},
		{"Ursula K. Le Guin", "Le Guin, Ursula K.", "French Le leads and keeps the surname"},
		{"Daphne du Maurier", "Du Maurier, Daphne", "French du leads and is capitalised at the front"},
		{"Thomas De Quincey", "De Quincey, Thomas", "capitalised particle leads, the BibTeX von-part rule"},
		{"Flannery O'Connor", "O'Connor, Flannery", "patronymic prefix is part of the surname"},
		{"Martin Luther King Jr.", "King, Martin Luther Jr.", "generational suffix follows the forename"},
		{"George R. R. Martin", "Martin, George R. R.", "ordinary name is unaffected"},
		{"Jo Nesbø", "Nesbø, Jo", "non-ASCII surname is unaffected"},
		{"刘慈欣", "刘慈欣", "CJK is already surname-first; inverting it is nonsense"},
		{"村上春樹", "村上春樹", "same"},
		{"Madonna", "Madonna", "single token"},
		{"Goethe, Johann Wolfgang von", "Goethe, Johann Wolfgang von", "already inverted; guessing again destroys it"},
		{"", "", "empty"},
	}
	for _, c := range cases {
		if got := SortName(c.in); got != c.want {
			t.Errorf("SortName(%q) = %q, want %q (%s)", c.in, got, c.want, c.rule)
		}
	}
}

// TestSortNameIsIdempotent guards the property that makes SortName safe to
// apply to a value that may already have been through it: the comma check has
// to catch its own output, or a second pass files the author under their
// forename.
func TestSortNameIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"Johann Wolfgang von Goethe", "Ursula K. Le Guin", "Vincent van Gogh",
		"Jose de la Cruz", "Martin Luther King Jr.", "Madonna", "刘慈欣",
	} {
		once := SortName(in)
		if twice := SortName(once); twice != once {
			t.Errorf("SortName is not idempotent for %q: once %q, twice %q", in, once, twice)
		}
	}
}
