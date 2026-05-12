package importer

import "testing"

// TestAuthorMatch_CoAuthorSurnameOverlap is the regression test for #563. The
// old substring-based check let a filename naming a co-author who shares the
// monitored author's surname be claimed as the monitored author's work.
// "Adam Reid" parsed from a filename must NOT be treated as belonging to
// "Rachel Reid".
func TestAuthorMatch_CoAuthorSurnameOverlap(t *testing.T) {
	cases := []struct {
		name         string
		bookAuthor   string // the monitored / library author
		parsedAuthor string // parsed from filename or directory
		want         bool
	}{
		{
			name:         "co-author shares surname only",
			bookAuthor:   "Rachel Reid",
			parsedAuthor: "Adam Reid",
			want:         false,
		},
		{
			name:         "parsed author is full co-author list",
			bookAuthor:   "Rachel Reid",
			parsedAuthor: "Rachel Larsen, Adam Reid, and Ozi Akturk",
			want:         false, // "larsen" not in "Rachel Reid"
		},
		{
			name:         "legitimate exact match",
			bookAuthor:   "Rachel Reid",
			parsedAuthor: "Rachel Reid",
			want:         true,
		},
		{
			name:         "legitimate surname-only filename hint",
			bookAuthor:   "Rachel Reid",
			parsedAuthor: "Reid",
			want:         true,
		},
		{
			name:         "initials dropped: George R. R. Martin → George Martin",
			bookAuthor:   "George R. R. Martin",
			parsedAuthor: "George Martin",
			want:         true,
		},
		{
			name:         "initials in parsed name dropped",
			bookAuthor:   "George Martin",
			parsedAuthor: "George R. R. Martin",
			want:         true, // r/r initials dropped; both "george" and "martin" match
		},
		{
			name:         "single-name pseudonym",
			bookAuthor:   "Plato",
			parsedAuthor: "Plato",
			want:         true,
		},
		{
			name:         "single-name pseudonym mismatch",
			bookAuthor:   "Plato",
			parsedAuthor: "Aristotle",
			want:         false,
		},
		{
			name:         "hyphenated first name",
			bookAuthor:   "Mary-Kate Olsen",
			parsedAuthor: "Mary-Kate Olsen",
			want:         true,
		},
		{
			name:         "hyphenated last name match",
			bookAuthor:   "Joyce Carol Oates-Smith",
			parsedAuthor: "Joyce Oates-Smith",
			want:         true,
		},
		{
			name:         "empty parsed author is permissive",
			bookAuthor:   "Anyone",
			parsedAuthor: "",
			want:         true,
		},
		{
			name:         "parsed author is only an initial",
			bookAuthor:   "R. R. Haywood",
			parsedAuthor: "R",
			want:         true, // no significant tokens — can't disprove
		},
		{
			name:         "substring no longer matches: 'reid' inside 'reiderson'",
			bookAuthor:   "Bob Reiderson",
			parsedAuthor: "Rachel Reid",
			want:         false, // word-boundary: \breid\b does NOT match "reiderson"
		},
		{
			name:         "case-insensitive match",
			bookAuthor:   "rachel reid",
			parsedAuthor: "Rachel Reid",
			want:         true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := authorMatch(tc.bookAuthor, tc.parsedAuthor)
			if got != tc.want {
				t.Errorf("authorMatch(%q, %q) = %v, want %v",
					tc.bookAuthor, tc.parsedAuthor, got, tc.want)
			}
		})
	}
}

// TestAuthorMatch_RegexCacheReuse verifies the regex cache is consulted on
// repeated calls (sanity check; cache correctness is covered indirectly by
// the case-table tests above).
func TestAuthorMatch_RegexCacheReuse(t *testing.T) {
	// First call populates the cache.
	_ = authorMatch("Rachel Reid", "Rachel Reid")
	if _, ok := authorTokenRegexCache.Load("rachel"); !ok {
		t.Error("expected 'rachel' regex to be cached after first call")
	}
	if _, ok := authorTokenRegexCache.Load("reid"); !ok {
		t.Error("expected 'reid' regex to be cached after first call")
	}
}
