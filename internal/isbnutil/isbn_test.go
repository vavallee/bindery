package isbnutil

import "testing"

func TestNormalize(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "isbn10 lowercase x check digit", raw: "3-453-30523-x", want: "345330523X"},
		{name: "isbn13 hyphen separators", raw: "978-0-307-47472-8", want: "9780307474728"},
		{name: "isbn13 space separators", raw: "978 0 307 47472 8", want: "9780307474728"},
		{name: "interior x preserved", raw: "978X0307474728", want: "978X0307474728"},
		{name: "early x preserved", raw: "97X80307474728", want: "97X80307474728"},
		{name: "multiple x preserved", raw: "978X030747472X", want: "978X030747472X"},
		{name: "invalid letters preserved", raw: "ISBN 9780307474728", want: "ISBN9780307474728"},
		// The dash class, which a user pastes far more often than a plain
		// hyphen. Each of these used to survive normalization and make the
		// value fail every length check downstream.
		{name: "en dash separators", raw: "978–0–307–47472–8", want: "9780307474728"},
		{name: "figure dash separators", raw: "978‒0‒307‒47472‒8", want: "9780307474728"},
		{name: "non-breaking hyphen separators", raw: "978‑0‑307‑47472‑8", want: "9780307474728"},
		{name: "minus sign separators", raw: "978−0−307−47472−8", want: "9780307474728"},
		{name: "soft hyphens", raw: "978\u00ad0\u00ad307\u00ad47472\u00ad8", want: "9780307474728"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.raw); got != tt.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestValidISBN10 checks the mod-11 check digit against real ISBN-10s,
// including the 'X' (value 10) case, and against inputs that differ from a
// valid one only in a way the check digit is meant to catch: a single mistyped
// digit and a transposed pair.
func TestValidISBN10(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "dune", raw: "0441172717", want: true},
		{name: "dune hyphenated", raw: "0-441-17271-7", want: true},
		{name: "the hitchhikers guide", raw: "0345391802", want: true},
		{name: "x check digit", raw: "080442957X", want: true},
		{name: "x check digit lowercase", raw: "0-8044-2957-x", want: true},
		{name: "x check digit hyphenated", raw: "3-453-30523-X", want: true},
		{name: "single digit typo", raw: "0441172718", want: false},
		{name: "transposed digits", raw: "0441127717", want: false},
		{name: "x in an interior position", raw: "04411X2717", want: false},
		{name: "too short", raw: "044117271", want: false},
		{name: "too long", raw: "04411727177", want: false},
		{name: "empty", raw: "", want: false},
		{name: "not digits", raw: "not-an-isbn", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidISBN10(tt.raw); got != tt.want {
				t.Fatalf("ValidISBN10(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestValidISBN13 checks the EAN-13 alternating 1/3 weighting. 9780306406157 /
// 9780306406152 are the standard valid/invalid pair; the 979 rows matter
// because that prefix is now issued and its check digit is computed the same
// way (the suite previously carried a made-up 979 number whose check digit did
// not verify).
func TestValidISBN13(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "dune", raw: "9780441172719", want: true},
		{name: "dune hyphenated", raw: "978-0-441-17271-9", want: true},
		{name: "check digit zero", raw: "9780132350884", want: true},
		{name: "979 prefix", raw: "979-10-90636-07-1", want: true},
		{name: "wrong check digit", raw: "9780306406152", want: false},
		{name: "right check digit", raw: "9780306406157", want: true},
		{name: "transposed digits", raw: "9780441712719", want: false},
		{name: "not a bookland prefix", raw: "4006381333931", want: false},
		{name: "isbn10 length", raw: "0441172717", want: false},
		{name: "letter inside", raw: "978X441172719", want: false},
		{name: "empty", raw: "", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidISBN13(tt.raw); got != tt.want {
				t.Fatalf("ValidISBN13(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestToISBN13(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "isbn13 passthrough", raw: "9780441172719", want: "9780441172719"},
		{name: "isbn13 hyphenated", raw: "978-0-441-17271-9", want: "9780441172719"},
		{name: "isbn13 979 prefix", raw: "979-10-90636-07-1", want: "9791090636071"},
		{name: "isbn13 en dash", raw: "978–0–441–17271–9", want: "9780441172719"},
		{name: "isbn10 converts", raw: "0441172717", want: "9780441172719"},
		{name: "isbn10 hyphenated", raw: "0-441-17271-7", want: "9780441172719"},
		{name: "isbn10 x check digit", raw: "3-453-30523-x", want: "9783453305236"},
		{name: "empty", raw: "", want: ""},
		{name: "not an isbn", raw: "not-an-isbn", want: ""},
		{name: "wrong length", raw: "12345", want: ""},
		{name: "13 digits without bookland prefix", raw: "1234567890123", want: ""},
		{name: "interior letter", raw: "978X0307474728", want: ""},
		{name: "isbn10 interior x", raw: "04411X2717", want: ""},

		// The rejections this function did not used to make. An ISBN-10's
		// check digit was thrown away before conversion, so a mistyped core
		// came back out as a well-formed ISBN-13 for a different book, and a
		// mistyped ISBN-13 was passed through untouched.
		{name: "isbn10 with a mistyped digit", raw: "0441172718", want: ""},
		{name: "isbn10 with transposed digits", raw: "0441127717", want: ""},
		{name: "isbn13 with a mistyped check digit", raw: "9780306406152", want: ""},
		{name: "isbn13 with transposed digits", raw: "9780441712719", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToISBN13(tt.raw); got != tt.want {
				t.Fatalf("ToISBN13(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestToISBN13RejectsWithoutInventing is the property the check digit is there
// for: no input ToISBN13 accepts may come back as an ISBN-13 whose own check
// digit is wrong, and a valid ISBN-10 and its ISBN-13 twin must agree.
func TestToISBN13RejectsWithoutInventing(t *testing.T) {
	for _, raw := range []string{
		"0441172717", "0-441-17271-7", "080442957X", "9780441172719",
		"0441172718", "9780306406152", "not-an-isbn", "",
	} {
		got := ToISBN13(raw)
		if got != "" && !ValidISBN13(got) {
			t.Fatalf("ToISBN13(%q) = %q, which is not a valid ISBN-13", raw, got)
		}
	}
	if a, b := ToISBN13("0441172717"), ToISBN13("9780441172719"); a != b {
		t.Fatalf("ISBN-10 and ISBN-13 forms of Dune disagree: %q vs %q", a, b)
	}
}

func TestNormalizeASIN(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "already canonical", raw: "B08XYZ1234", want: "B08XYZ1234"},
		{name: "lowercase", raw: "b08xyz1234", want: "B08XYZ1234"},
		{name: "mixed case with surrounding space", raw: "  b08Xyz1234\n", want: "B08XYZ1234"},
		{name: "empty", raw: "   ", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeASIN(tt.raw); got != tt.want {
				t.Fatalf("NormalizeASIN(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestExtract covers the union of what the EPUB OPF reader and the DNB MARC
// reader used to scan for with their own code, plus the shapes each of them
// used to get wrong.
func TestExtract(t *testing.T) {
	for _, tt := range []struct {
		name    string
		raw     string
		want13  string
		want10  string
		comment string
	}{
		// EPUB dc:identifier shapes.
		{name: "urn isbn 13", raw: "urn:isbn:9780345472199", want13: "9780345472199"},
		{name: "isbn prefix 10", raw: "isbn:0345472195", want10: "0345472195"},
		{name: "bare isbn13", raw: "9780345472199", want13: "9780345472199"},
		{name: "hyphenated isbn10", raw: "0-345-47219-5", want10: "0345472195"},
		{name: "uuid is not an isbn", raw: "urn:uuid:1234"},
		{name: "x check digit", raw: "123456789X", want10: "123456789X"},

		// MARC 020 $a shapes.
		{name: "marc plain", raw: "9783499015717", want13: "9783499015717"},
		{name: "marc binding qualifier", raw: "9783499015717 (pbk.)", want13: "9783499015717"},
		{name: "marc hyphenated isbn10", raw: "3-499-01571-X", want10: "349901571X"},
		{name: "marc padded", raw: "  9783446123456  ", want13: "9783446123456"},
		{name: "marc lowercase x", raw: "349901571x", want10: "349901571X"},

		// Shapes the old scanners got wrong. DNB stopped at the first space,
		// so a space-separated ISBN came back as "978" and was dropped; the
		// EPUB reader kept every digit in the string, so a qualifier carrying
		// its own digits ran into the ISBN and made it the wrong length.
		{name: "space separated", raw: "978 3 446 12345 6", want13: "9783446123456"},
		{name: "qualifier with digits", raw: "9783499015717 (Bd. 2)", want13: "9783499015717"},
		{name: "en dash separated", raw: "978–3–446–12345–6", want13: "9783446123456"},

		// A digit run glued to letters is part of a longer word, not an
		// identifier: the tail of a UUID must not become an ISBN-10.
		{name: "uuid tail", raw: "urn:uuid:1e2b3c4d-5678-90ab-cdef-1234567890ab"},
		{name: "hyphenated uuid", raw: "urn:uuid:12345678-1234-5678-1234-567812345678"},

		// Nothing usable.
		{name: "empty", raw: ""},
		{name: "no digits", raw: "no digits here"},
		{name: "wrong length", raw: "12345"},
		{name: "thirteen digits, not bookland", raw: "1234567890123"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got13, got10 := Extract(tt.raw)
			if got13 != tt.want13 || got10 != tt.want10 {
				t.Fatalf("Extract(%q) = (%q, %q), want (%q, %q)", tt.raw, got13, got10, tt.want13, tt.want10)
			}
			if got13 != "" && got10 != "" {
				t.Fatalf("Extract(%q) returned both forms", tt.raw)
			}
		})
	}
}
