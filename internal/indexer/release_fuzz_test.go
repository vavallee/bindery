package indexer

import (
	"strings"
	"testing"
)

// FuzzParseRelease exercises the release-title parser with arbitrary input.
// ParseRelease runs on untrusted indexer result titles throughout the search,
// decision, queue, and scheduler paths (it is distinct from the importer's
// ParseFilename, which handles on-disk names), so it must never panic and must
// return structurally sane output regardless of input. All regexes involved
// are package-level precompiled; nothing attacker-controlled is compiled.
func FuzzParseRelease(f *testing.F) {
	seeds := []string{
		"",
		"Mary.Doria.Russell.-.The.Sparrow.(1996).RETAIL.EPUB-GROUP",
		"Dune.Messiah.UNABRIDGED.2020.m4b",
		"The.Sparrow.ABRIDGED.mp3",
		"The.Sparrow.9780449912553.epub",
		"978-0 449 91255 3 The Sparrow",
		"Dune.Herbert.[B0036S4B2G].m4b",
		"Der.Schwarm.Frank.Schaetzing.Hoerbuch.2004.MP3-GRP",
		"Ender's Game – Orson Scott Card | RETAIL (1985) azw3",
		"....___...|||[[[]]]()()-",
		strings.Repeat("a.", 400) + "-LONGTAIL",
		"日本語のタイトル 2021 epub",
		"UNABRIDGEDABRIDGED retail 1899 2100",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, title string) {
		p := ParseRelease(title) // must not panic on any input

		// Normalized is trimmed with collapsed single-space separators.
		if p.Normalized != strings.TrimSpace(p.Normalized) {
			t.Fatalf("Normalized not trimmed: %q", p.Normalized)
		}
		if strings.Contains(p.Normalized, "  ") {
			t.Fatalf("Normalized contains a double space: %q", p.Normalized)
		}

		// Year comes from a \b(19|20)\d{2}\b match.
		if p.Year != 0 && (p.Year < 1900 || p.Year > 2099) {
			t.Fatalf("Year out of range: %d for %q", p.Year, title)
		}

		// ISBN is normalised to digits only, 13 long, 978/979 prefixed.
		if p.ISBN != "" {
			if len(p.ISBN) != 13 || strings.Trim(p.ISBN, "0123456789") != "" ||
				(!strings.HasPrefix(p.ISBN, "978") && !strings.HasPrefix(p.ISBN, "979")) {
				t.Fatalf("malformed ISBN %q for %q", p.ISBN, title)
			}
		}

		// ASIN is a 10-char B-prefixed uppercase alphanumeric token.
		if p.ASIN != "" {
			if len(p.ASIN) != 10 || p.ASIN[0] != 'B' ||
				strings.Trim(p.ASIN[1:], "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
				t.Fatalf("malformed ASIN %q for %q", p.ASIN, title)
			}
		}

		// Format is one of the recognised tokens or empty.
		if p.Format != "" {
			ok := false
			for _, ft := range formatTokens {
				if p.Format == ft {
					ok = true
					break
				}
			}
			if !ok {
				t.Fatalf("unknown Format %q for %q", p.Format, title)
			}
		}

		// Abridged is defined as "ABRIDGED present and not UNABRIDGED".
		if p.Abridged && p.Unabridged {
			t.Fatalf("Abridged and Unabridged both set for %q", title)
		}

		// ReleaseGroup is the trailing -GROUP alphanumeric capture.
		if p.ReleaseGroup != "" && strings.Trim(p.ReleaseGroup,
			"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			t.Fatalf("non-alphanumeric ReleaseGroup %q for %q", p.ReleaseGroup, title)
		}
	})
}
