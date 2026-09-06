package importer

import "testing"

// TestLookupAuthorMatchBindsParsedFilenames walks the path that turns a
// scanned file into a library entry: ParseFilename pulls an author out of a
// release name, and lookupAuthorMatch has to bind it to the catalogue row for
// the same person and to nothing else.
//
// It is here because the fallback that decides those binds changed from a raw
// Jaro-Winkler score to textutil.MatchAuthorName. The catalogue deliberately
// holds two traps: "Nick Lang" beside "Nick Lane" (a short surname, one letter
// apart, which the score matched at 0.9333) and 刘慈欣 beside "Liu Cixin" (a
// romanisation, which is an alias question rather than a name comparison).
func TestLookupAuthorMatchBindsParsedFilenames(t *testing.T) {
	catalogue := []string{
		"Jo Nesbø", "Brandon Sanderson", "J. R. R. Tolkien", "Nick Lane",
		"Heinrich Böll", "George R.R. Martin", "Ursula K. Le Guin", "刘慈欣",
		"Liu Cixin", "Nick Lang", "Christopher Ross",
	}
	cases := []struct {
		file string
		want string
	}{
		{"The Way of Kings - Brandon Sanderson.epub", "Brandon Sanderson"},
		{"The Hobbit - Tolkien, J.R.R..epub", "J. R. R. Tolkien"},
		{"The Hobbit - J.R.R. Tolkien.epub", "J. R. R. Tolkien"},
		{"The Hobbit - Tolkien.epub", "J. R. R. Tolkien"},
		{"The Vital Question - Nick Lane.epub", "Nick Lane"},
		{"The Vital Question - N. Lane.epub", "Nick Lane"},
		{"Ansichten eines Clowns - Heinrich Boell.epub", "Heinrich Böll"},
		{"A Game of Thrones - George R R Martin.epub", "George R.R. Martin"},
		{"The Dispossessed - Ursula K. Le Guin.epub", "Ursula K. Le Guin"},
		{"The Three-Body Problem - Liu Cixin.epub", "Liu Cixin"},
		{"The Snowman - Jo Nesbo.epub", "Jo Nesbø"},
	}
	for _, tc := range cases {
		parsed := ParseFilename(tc.file).Author
		var bound []string
		for _, c := range catalogue {
			if lookupAuthorMatch(parsed, c) {
				bound = append(bound, c)
			}
		}
		if len(bound) != 1 || bound[0] != tc.want {
			t.Errorf("%q parsed author %q binds to %v, want exactly [%q]", tc.file, parsed, bound, tc.want)
		}
	}
}
