package openlibrary

import "testing"

// TestShouldFilterOLNoise_HistoryAndCriticismAndBiography verifies the
// "history and criticism" / "authors, biography" subject phrases (added
// alongside the narrower pre-existing "literary criticism" / "criticism and
// interpretation" phrases, which were confirmed not to substring-match this
// wording). Fixtures are real OpenLibrary work titles and subjects pulled
// from J.R.R. Tolkien's actual author-works catalogue during PR validation:
// companion guides, art books, and a biography that OpenLibrary credits him
// as "author" on despite being written about him, not by him.
func TestShouldFilterOLNoise_HistoryAndCriticismAndBiography(t *testing.T) {
	cases := []struct {
		title    string
		subjects []string
		want     bool
	}{
		{
			title:    "Tolkien's World",
			subjects: []string{"English Fantasy literature", "History and criticism", "Middle Earth (Imaginary place)"},
			want:     true,
		},
		{
			title:    "Tolkien's Middle-Earth",
			subjects: []string{"Middle earth (imaginary place)", "Fantasy fiction, history and criticism"},
			want:     true,
		},
		{
			title:    "Essays Presented to Charles Williams",
			subjects: []string{"English essays", "Literature", "History and criticism", "Galleys", "Marriage", "Literature, history and criticism"},
			want:     true,
		},
		{
			title:    "J. R. R. Tolkien Companion and Guide : Volume 2",
			subjects: []string{"Tolkien, j, r. r. (john ronald ruel), 1892-1973", "Authors, english", "Authors, biography"},
			want:     true,
		},
		{
			title:    "J. R. R. Tolkien",
			subjects: []string{"Tolkien, j, r. r. (john ronald ruel), 1892-1973", "Authors, english", "Authors, biography"},
			want:     true,
		},
		{
			// A real autobiography must NOT be caught: "biography" is a
			// substring of "autobiography", which is exactly why the added
			// phrase is the specific compound "authors, biography" and not
			// bare "biography".
			title:    "An Autobiography",
			subjects: []string{"Autobiography", "Authors, English"},
			want:     false,
		},
		{
			// A real single-author work with no noise-signal subjects must
			// still pass through unfiltered.
			title:    "The Silmarillion",
			subjects: []string{"Fiction, fantasy, general", "Middle Earth (Imaginary place)"},
			want:     false,
		},
	}
	for _, c := range cases {
		if got := shouldFilterOLNoise(c.title, c.subjects); got != c.want {
			t.Errorf("shouldFilterOLNoise(%q, %v) = %v, want %v", c.title, c.subjects, got, c.want)
		}
	}
}
