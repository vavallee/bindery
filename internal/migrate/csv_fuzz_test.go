package migrate

import (
	"strings"
	"testing"
)

// FuzzParseCSVRows runs the CSV importer's row parser over arbitrary input.
// CSV uploads are user-supplied and parsed before any validation, so the parser
// must never panic and must never return more rows than the input could contain.
// rowFromFields is exercised through the same path.
func FuzzParseCSVRows(f *testing.F) {
	seeds := []string{
		"",
		"name\nBrandon Sanderson\n",
		"name,foreign_id,searchOnAdd\nAuthor,OL123A,true\n",
		"Andy Weir,,\n",
		"a,b,c,d,e,f,g\n",
		"\"unterminated quote\nNextLine,x",
		"name\n\"\"\"\"\"\"\n",
		strings.Repeat("a,b,c\n", 200),
		"\x00\x01,\x02\n",
		"héllo,wörld\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data string) {
		rows, err := parseCSVRows(strings.NewReader(data)) // must not panic
		if err != nil {
			return // malformed CSV is a legitimate, handled outcome
		}
		// A parsed row count can't exceed the number of newlines + 1; anything
		// more signals a construction bug.
		maxRows := strings.Count(data, "\n") + 1
		if len(rows) > maxRows {
			t.Fatalf("parsed %d rows from input with at most %d lines", len(rows), maxRows)
		}
	})
}
