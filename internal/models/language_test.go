package models

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"testing"
)

func TestParseAllowedLanguages(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"any", nil},
		{"ANY", nil},
		{"eng", []string{"eng"}},
		{"eng,fre,ger", []string{"eng", "fre", "ger"}},
		{" Eng , FRE ,  ger ", []string{"eng", "fre", "ger"}},
		{"eng,,fre", []string{"eng", "fre"}},
		// A single "any" anywhere short-circuits to no filter — having a
		// mixed list with "any" in it is contradictory and we treat the
		// broader setting as the user's real intent.
		{"eng,any,fre", nil},
	}
	for _, tc := range cases {
		got := ParseAllowedLanguages(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseAllowedLanguages(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeLanguageCode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{"en", "eng"},
		{"EN", "eng"},
		{"en-US", "eng"},
		{"pt_BR", "por"},
		{"zh-Hans", "chi"},
		{"de", "ger"},
		// Already three-letter: passed through lowercased unchanged.
		{"eng", "eng"},
		{"GER", "ger"},
		// Unknown two-letter code round-trips rather than being dropped.
		{"xx", "xx"},

		// #2463. ISO 639-2/T folds onto the /B form Bindery stores and
		// filters on; both spellings are legal and providers emit either.
		{"deu", "ger"},
		{"fra", "fre"},
		{"nld", "dut"},
		{"ces", "cze"},
		{"zho", "chi"},
		{"ron", "rum"},
		{"ell", "gre"},
		{"deu-DE", "ger"},
		// A language written out as a word, which is how Audible and Audnex
		// report it and how release names carry it.
		{"German", "ger"},
		{"Deutsch", "ger"},
		{"english", "eng"},
		{"Português", "por"},
		{"magyar", "hun"},
		// Still not a language, and still round-trips rather than vanishing.
		{"zulu", "zulu"},
		{"not a language", "not a language"},
	}
	for _, tc := range cases {
		if got := NormalizeLanguageCode(tc.in); got != tc.want {
			t.Errorf("NormalizeLanguageCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsLanguageAllowed(t *testing.T) {
	cases := []struct {
		code        string
		allowed     []string
		unknownFail bool
		want        bool
	}{
		{"eng", nil, false, true},
		{"eng", nil, true, true},
		{"", []string{"eng"}, false, true},
		{"", []string{"eng"}, true, false},
		{"eng", []string{"eng"}, false, true},
		{"ENG", []string{"eng"}, false, true},
		{" eng ", []string{"eng"}, false, true},
		{"fre", []string{"eng"}, false, false},
		{"fre", []string{"eng", "fre"}, false, true},
		{"ger", []string{"eng", "fre"}, false, false},
		// #1729: provider vocabularies must converge on the profile's. Google
		// Books returns ISO 639-1 ("en", "en-US"); a profile allowing "eng"
		// must accept them.
		{"en", []string{"eng"}, false, true},
		{"en-US", []string{"eng"}, false, true},
		{"EN", []string{"eng"}, false, true},
		{"pt_BR", []string{"por"}, false, true},
		// The allowed side is normalized too, so a profile written in
		// two-letter codes still filters correctly.
		{"eng", []string{"en"}, false, true},
		// A genuinely different language is still rejected.
		{"de", []string{"eng"}, false, false},
		{"fr-CA", []string{"eng"}, false, false},
		// Unknown-language behavior is unchanged by normalization.
		{"", []string{"en"}, false, true},
		{"", []string{"en"}, true, false},
	}
	for _, tc := range cases {
		got := IsLanguageAllowed(tc.code, tc.allowed, tc.unknownFail)
		if got != tc.want {
			t.Errorf("IsLanguageAllowed(%q, %v, unknownFail=%v) = %v, want %v", tc.code, tc.allowed, tc.unknownFail, got, tc.want)
		}
	}
}

// metadataTabPath points at the metadata profile editor, relative to this
// package directory (go test runs with the package dir as cwd).
const metadataTabPath = "../../web/src/pages/settings/MetadataTab.tsx"

// editorLanguageCodes returns the ISO 639-2/B codes KNOWN_LANGUAGES offers as
// checkboxes in the metadata profile editor.
func editorLanguageCodes(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(metadataTabPath)
	if err != nil {
		t.Fatalf("reading %s: %v (this guard needs the full repo checkout)", metadataTabPath, err)
	}
	block := regexp.MustCompile(`const KNOWN_LANGUAGES[^=]*=\s*\[([\s\S]*?)\n\]`).FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatalf("could not find `const KNOWN_LANGUAGES = [...]` in %s; if it was renamed or moved, update this drift guard to follow it", metadataTabPath)
	}
	var offered []string
	for _, m := range regexp.MustCompile(`code:\s*'([^']+)'`).FindAllStringSubmatch(block[1], -1) {
		offered = append(offered, m[1])
	}
	if len(offered) == 0 {
		t.Fatalf("`const KNOWN_LANGUAGES` in %s parsed to an empty list", metadataTabPath)
	}
	return offered
}

// TestNormalizedLanguagesStayInTheEditorVocabulary is the property behind
// #2463: whatever spelling of a language reaches NormalizeLanguageCode, the
// code it comes out as has to be one an operator could have ticked in the
// profile editor, or the filter compares a normalized book against a profile
// written in a vocabulary it can never match.
//
// The expected exceptions below are the languages the alias tables know but
// the editor does not offer, and they are listed rather than tolerated so that
// widening the tables stays a decision. They are safe precisely because each
// is a whole language the editor has no entry for at all, never a rival
// spelling of one it does offer: normalizing onto "cat" cannot steal a book
// away from a ticked box, because there is no Catalan box to steal it from.
// The editor's list is pinned to what the release-name filter can produce
// (TestMetadataEditorLanguageVocabulary in internal/indexer), so it can only
// grow once someone adds a release marker they can actually read; until then a
// Catalan book is unfiltered rather than misfiltered.
func TestNormalizedLanguagesStayInTheEditorVocabulary(t *testing.T) {
	offered := editorLanguageCodes(t)

	// Languages the tables normalize onto that the editor cannot offer yet.
	expectedUnoffered := []string{
		// 639-1 and name aliases for languages Audible reports but no
		// release marker names.
		"cat", "fin", "gre", "hun", "lat", "rum",
		// The remaining ISO 639-2 T/B pairs. Folding these is free: the /B
		// side is the standard's own spelling of the same language.
		"alb", "arm", "baq", "bur", "geo", "ice", "mac", "mao", "may",
		"per", "slo", "tib", "wel",
	}

	produced := map[string]bool{}
	for _, table := range []map[string]string{iso639TwoLetterToB, iso639TermToB, iso639NameToB} {
		for in, out := range table {
			// Every table has to agree with the finished canonicaliser, or
			// one of them is a second opinion again.
			if got := NormalizeLanguageCode(in); got != out {
				t.Errorf("a table maps %q to %q but NormalizeLanguageCode(%q) = %q", in, out, in, got)
			}
			// And no table may produce a code another table would rewrite.
			if got := NormalizeLanguageCode(out); got != out {
				t.Errorf("a table produces %q, which NormalizeLanguageCode then rewrites to %q", out, got)
			}
			produced[out] = true
		}
	}

	var unoffered []string
	for code := range produced {
		if !slices.Contains(offered, code) {
			unoffered = append(unoffered, code)
		}
	}
	slices.Sort(unoffered)
	slices.Sort(expectedUnoffered)
	if !slices.Equal(unoffered, expectedUnoffered) {
		t.Errorf("codes the alias tables produce but KNOWN_LANGUAGES in %s does not offer:\n got %v\nwant %v\n"+
			"A new entry here is a language a normalized value can land on that no operator can select. "+
			"Either add it to the editor (which needs a marker in releaseLanguageTags first) or drop the alias.",
			metadataTabPath, unoffered, expectedUnoffered)
	}

	// The other direction: a code the editor offers must survive
	// normalization untouched, or ticking its box filters on something else.
	for _, code := range offered {
		if got := NormalizeLanguageCode(code); got != code {
			t.Errorf("KNOWN_LANGUAGES offers %q but NormalizeLanguageCode rewrites it to %q", code, got)
		}
	}
}
