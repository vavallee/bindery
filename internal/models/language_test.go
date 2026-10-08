package models

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
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
		// Bokmål and Nynorsk fold onto the "nor" a profile stores.
		{"nob", "nor"},
		{"nno", "nor"},
		{"nb", "nor"},
		{"nn-NO", "nor"},
		// Withdrawn ISO 639-1 codes older EPUB tools still write, folded
		// onto the current ones rather than passed through raw.
		{"iw", "heb"},
		{"iw-IL", "heb"},
		{"in", "ind"},
		{"IN_id", "ind"},
		{"ji", "yid"},
		// A language written out as a word, which is how Audible and Audnex
		// report it and how release names carry it.
		{"German", "ger"},
		{"Deutsch", "ger"},
		{"english", "eng"},
		{"Português", "por"},
		{"magyar", "hun"},
		// #2998. The rest of ISO 639-1, which EPUBs use, and the Chinese
		// individual languages.
		{"uk", "ukr"},
		{"he", "heb"},
		{"sk", "slo"},
		{"fa-IR", "per"},
		{"cy", "wel"},
		{"is", "ice"},
		{"sr-Latn", "srp"},
		{"cmn", "chi"},
		{"yue-HK", "chi"},
		{"NOB", "nor"},
		{"nb-NO", "nor"},
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

func TestLanguageCodeVariants(t *testing.T) {
	cases := []struct {
		name  string
		codes []string
		want  []string
	}{
		{name: "english", codes: []string{"eng"}, want: []string{"en", "eng"}},
		{name: "bibliographic and terminology", codes: []string{"fre", "ger"}, want: []string{"de", "deu", "fr", "fra", "fre", "ger"}},
		{name: "normalizes input", codes: []string{"EN-us", "fra"}, want: []string{"en", "en-us", "eng", "fr", "fra", "fre"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LanguageCodeVariants(tc.codes); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("LanguageCodeVariants(%v) = %v, want %v", tc.codes, got, tc.want)
			}
		})
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
		// The rest of ISO 639-1 (#2998). An EPUB tagged "uk" has to read as
		// Ukrainian, not as no language, or the import language check lets
		// it through an English only profile.
		"aar", "abk", "afr", "aka", "amh", "arg", "asm", "ava", "ave", "aym",
		"aze", "bak", "bam", "bel", "ben", "bis", "bos", "bre", "bul", "cha",
		"che", "chu", "chv", "cor", "cos", "cre", "div", "dzo", "epo", "est",
		"ewe", "fao", "fij", "fry", "ful", "gla", "gle", "glg", "glv", "grn",
		"guj", "hat", "hau", "heb", "her", "hmo", "hrv", "ibo", "ido", "iii",
		"iku", "ile", "ina", "ind", "ipk", "jav", "kal", "kan", "kas", "kau",
		"kaz", "khm", "kik", "kin", "kir", "kom", "kon", "kua", "kur", "lao",
		"lav", "lim", "lin", "lit", "ltz", "lub", "lug", "mah", "mal", "mar",
		"mlg", "mlt", "mon", "nau", "nav", "nbl", "nde", "ndo", "nep", "nya",
		"oci", "oji", "ori", "orm", "oss", "pan", "pli", "pus", "que", "roh",
		"run", "sag", "san", "sin", "slv", "sme", "smo", "sna", "snd", "som",
		"sot", "srd", "srp", "ssw", "sun", "swa", "tah", "tam", "tat", "tel",
		"tgk", "tgl", "tha", "tir", "ton", "tsn", "tso", "tuk", "twi", "uig",
		"ukr", "urd", "uzb", "ven", "vie", "vol", "wln", "wol", "xho", "yid",
		"yor", "zha", "zul",
	}

	produced := map[string]bool{}
	for _, table := range languageAliasTables() {
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

// TestLanguageName covers the names an import rejection uses (#2998): every
// spelling of a code gives one English name, and a code with no name comes
// back normalised rather than empty.
func TestLanguageName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sv", "Swedish"},
		{"swe", "Swedish"},
		{"sv-SE", "Swedish"},
		{"en_GB", "English"},
		{"deu", "German"},
		{"nob", "Norwegian"},
		{"uk", "Ukrainian"},
		{"cmn", "Chinese"},
		{"zulu", "zulu"},
		{"XX", "xx"},
	}
	for _, tc := range cases {
		if got := LanguageName(tc.in); got != tc.want {
			t.Errorf("LanguageName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestLanguageNamesCoverEveryNormalisedCode keeps the display table in step
// with the alias tables: a language Bindery can normalise onto must also have
// a name, or a rejection message reads "file declares alb".
func TestLanguageNamesCoverEveryNormalisedCode(t *testing.T) {
	for _, table := range languageAliasTables() {
		for _, code := range table {
			if _, ok := languageNames[code]; !ok {
				t.Errorf("no English name for %q", code)
			}
		}
	}
}

// languageAliasTables is every table NormalizeLanguageCode reads.
func languageAliasTables() []map[string]string {
	return []map[string]string{iso639TwoLetterToB, iso639TermToB, iso639NameToB, iso639NorwegianToB, iso639ChineseToB}
}

// TestTwoLetterTableCoversISO6391 pins the two letter table to the whole of
// ISO 639-1 (#2998). The table used to hold 25 codes, so an EPUB tagged "uk",
// "he" or "sk" normalised to a code nothing recognised and was treated as
// declaring no language at all.
func TestTwoLetterTableCoversISO6391(t *testing.T) {
	// The 183 current ISO 639-1 codes ("bh" was withdrawn in 2021).
	iso6391 := strings.Fields(`
		aa ab ae af ak am an ar as av ay az ba be bg bi bm bn bo br bs ca ce ch
		co cr cs cu cv cy da de dv dz ee el en eo es et eu fa ff fi fj fo fr fy
		ga gd gl gn gu gv ha he hi ho hr ht hu hy hz ia id ie ig ii ik io is it
		iu ja jv ka kg ki kj kk kl km kn ko kr ks ku kv kw ky la lb lg li ln lo
		lt lu lv mg mh mi mk ml mn mr ms mt my na nb nd ne ng nl nn no nr nv ny
		oc oj om or os pa pi pl ps pt qu rm rn ro ru rw sa sc sd se sg si sk sl
		sm sn so sq sr ss st su sv sw ta te tg th ti tk tl tn to tr ts tt tw ty
		ug uk ur uz ve vi vo wa wo xh yi yo za zh zu`)
	if len(iso6391) != 183 {
		t.Fatalf("fixture lists %d codes, want 183", len(iso6391))
	}
	for _, code := range iso6391 {
		b, ok := iso639TwoLetterToB[code]
		if !ok {
			t.Errorf("ISO 639-1 %q is missing from iso639TwoLetterToB", code)
			continue
		}
		if len(b) != 3 {
			t.Errorf("iso639TwoLetterToB[%q] = %q, want a three letter code", code, b)
		}
	}
	if len(iso639TwoLetterToB) != len(iso6391) {
		t.Errorf("iso639TwoLetterToB has %d entries, want %d", len(iso639TwoLetterToB), len(iso6391))
	}
}
