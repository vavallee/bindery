package importer

import (
	"strings"
	"testing"
)

// FuzzParseFilename exercises the release-name/filename parser with arbitrary
// input. ParseFilename runs on untrusted strings (torrent/NZB titles, on-disk
// names) throughout the import path, so it must never panic and must return
// sane, bounded output regardless of input. This is also Bindery's OpenSSF
// Scorecard "Fuzzing" signal (a Go native fuzz target).
func FuzzParseFilename(f *testing.F) {
	seeds := []string{
		"",
		"Andy Weir - Project Hail Mary (2021) [B08GB58KD5].epub",
		"The Way of Kings by Brandon Sanderson [Stormlight Archive #1].m4b",
		"01 - The Fellowship of the Ring.mobi",
		"978-0-13-468599-1 Some Title.pdf",
		"....___...",
		"[[[(((]]]))) #### 9780134685991",
		"Title – Author With En Dash.azw3",
		strings.Repeat("a ", 500) + "#12",
		"日本語のタイトル by 村上春樹.epub",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, path string) {
		got := ParseFilename(path) // must not panic on any input

		// FilePath echoes the input verbatim.
		if got.FilePath != path {
			t.Fatalf("FilePath mutated: got %q want %q", got.FilePath, path)
		}
		// No extracted field may be longer than the input — the parser only
		// slices substrings out, so anything longer signals a construction bug.
		// (Encoding is intentionally not asserted: ParseFilename passes bytes
		// through and does not promise to sanitise invalid UTF-8 from a hostile
		// filename; downstream JSON/DB layers handle that. It must not panic,
		// which is the property this fuzz target guards.)
		for name, v := range map[string]string{
			"Title": got.Title, "Author": got.Author, "Series": got.Series,
			"SeriesNumber": got.SeriesNumber, "ISBN": got.ISBN, "ASIN": got.ASIN,
			"Year": got.Year, "Format": got.Format,
		} {
			if len(v) > len(path) {
				t.Fatalf("%s longer than input (%d > %d) for %q: %q", name, len(v), len(path), path, v)
			}
		}
	})
}
