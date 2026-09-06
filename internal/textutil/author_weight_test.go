package textutil

import "testing"

// TestMatchAuthorNameWeightTable is the fitted weight table, written down as
// pairs. Each row isolates one field outcome against a known other field, so a
// change to any single weight fails exactly the rows it moves rather than
// producing a diffuse shift in which authors merge.
func TestMatchAuthorNameWeightTable(t *testing.T) {
	cases := []struct {
		name   string
		a, b   string
		weight float64
	}{
		// surname evidence, with the given name equal (+3) throughout.
		{"surname equal", "Neal Stephenson", "Neal Stephenson", 7},
		{"surname near-identical", "Brandon Sanderson", "Brandon Sandersen", 6},
		{"surname close and long", "Robert Jordan", "Robert Jordon", 4},
		{"surname close and short", "Christopher Ross", "Christopher Rose", -1},
		{"surname diverging", "Alice Jones", "Alice James", 2},
		{"surname conflicting", "Heinrich Böll", "Heinrich Mann", 0},
		{"surname across scripts", "刘慈欣", "Liu Cixin", 0},

		// given-name evidence, with the surname equal (+4) throughout.
		{"given initials compatible", "J.R.R. Tolkien", "John Ronald Reuel Tolkien", 5.5},
		{"given misspelled", "Micheal Smith", "Michael Smith", 5.5},
		{"given absent on one side", "Tolkien", "J.R.R. Tolkien", 4},
		{"given a different name", "John Smith", "Peter Smith", 1},

		// order swap, capped below the auto band whatever the fields say.
		{"bare order swap", "Stanley Paul", "Paul Stanley", authorSwapWeight},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchAuthorName(tc.a, tc.b)
			if got.Weight != tc.weight {
				t.Fatalf("MatchAuthorName(%q, %q).Weight = %.1f, want %.1f (kind %s)",
					tc.a, tc.b, got.Weight, tc.weight, kindNames[got.Kind])
			}
			if got.Kind != authorWeightKind(got.Weight) && got.Kind != AuthorMatchExact {
				t.Fatalf("MatchAuthorName(%q, %q) kind %s does not follow weight %.1f",
					tc.a, tc.b, kindNames[got.Kind], got.Weight)
			}
		})
	}
}

// TestAuthorWeightBands pins the band edges. They are inclusive lower bounds,
// so a pairing that scores exactly the auto weight auto-matches.
func TestAuthorWeightBands(t *testing.T) {
	cases := []struct {
		weight float64
		want   AuthorMatchKind
	}{
		{7, AuthorMatchFuzzyAuto},
		{AuthorMatchAutoWeight, AuthorMatchFuzzyAuto},
		{AuthorMatchAutoWeight - 0.5, AuthorMatchFuzzyAmbiguous},
		{AuthorMatchAmbiguousWeight, AuthorMatchFuzzyAmbiguous},
		{AuthorMatchAmbiguousWeight - 0.5, AuthorMatchNone},
		{-7, AuthorMatchNone},
	}
	for _, tc := range cases {
		if got := authorWeightKind(tc.weight); got != tc.want {
			t.Errorf("authorWeightKind(%.1f) = %s, want %s", tc.weight, kindNames[got], kindNames[tc.want])
		}
	}
	// An exact pairing must sit clear of the auto edge: nothing should be able
	// to demote it by half a point of some future field.
	if authorExactWeight <= AuthorMatchAutoWeight {
		t.Errorf("authorExactWeight %.1f must exceed the auto band edge %.1f", float64(authorExactWeight), AuthorMatchAutoWeight)
	}
	// The swap cap must land inside the ambiguous band by construction.
	if authorWeightKind(authorSwapWeight) != AuthorMatchFuzzyAmbiguous {
		t.Errorf("authorSwapWeight %.1f is not in the ambiguous band", float64(authorSwapWeight))
	}
}

// TestSplitAuthorForm covers the surname/given split MatchAuthorName scores
// with. SortName is not used: it is a display value, it flips on the last
// token with no particle handling, and it is being changed separately.
func TestSplitAuthorForm(t *testing.T) {
	cases := []struct {
		form, surname, given string
	}{
		{"john ronald reuel tolkien", "tolkien", "john ronald reuel"},
		{"j r r tolkien", "tolkien", "j r r"},
		{"tolkien", "tolkien", ""},
		{"", "", ""},
		// All non-Latin: no split at all, spaces closed up, because a CJK name
		// puts the family name first and is commonly written without one.
		{"刘慈欣", "刘慈欣", ""},
		{"村上 春樹", "村上春樹", ""},
	}
	for _, tc := range cases {
		got := splitAuthorForm(tc.form)
		if got.surname != tc.surname || got.given != tc.given {
			t.Errorf("splitAuthorForm(%q) = (%q, %q), want (%q, %q)",
				tc.form, got.surname, got.given, tc.surname, tc.given)
		}
	}

	// The comma form is handled upstream, by authorNameForms emitting both the
	// written order and the order the comma declares. The scorer takes the
	// best pairing, so "Goethe, Johann Wolfgang von" must offer a form whose
	// surname is "goethe".
	forms := authorNameForms("Goethe, Johann Wolfgang von", false)
	found := false
	for _, f := range forms {
		if splitAuthorForm(f).surname == "goethe" {
			found = true
		}
	}
	if !found {
		t.Errorf("authorNameForms(%q) = %v, no form splits to surname %q",
			"Goethe, Johann Wolfgang von", forms, "goethe")
	}
}

// TestAuthorNameFormsOrderPreserving checks that dropping the last-first swap
// leaves everything else — including the order a comma declares — in place,
// and that the swapped set is still exactly what it always was.
func TestAuthorNameFormsOrderPreserving(t *testing.T) {
	ordered := authorNameForms("R.R. Haywood", false)
	for _, f := range ordered {
		if f == "haywood r r" || f == "haywood rr" {
			t.Errorf("authorNameForms(%q, false) = %v, must not carry a last-first form", "R.R. Haywood", ordered)
		}
	}
	if len(ordered) == 0 || ordered[0] != "r r haywood" {
		t.Errorf("authorNameForms(%q, false) = %v, want the base form first", "R.R. Haywood", ordered)
	}

	// A comma declares an order, so both readings are order-preserving.
	comma := authorNameForms("Haywood, R.R.", false)
	want := map[string]bool{"haywood r r": false, "r r haywood": false}
	for _, f := range comma {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for form, seen := range want {
		if !seen {
			t.Errorf("authorNameForms(%q, false) = %v, missing %q", "Haywood, R.R.", comma, form)
		}
	}
}
