package models

import (
	"slices"
	"strings"
)

// DefaultMetadataProfileID is the ID of the seeded "Standard" profile created
// in migration 003. Authors with no explicit profile fall back to it so the
// language filter always has a value to consult.
const DefaultMetadataProfileID int64 = 1

// ParseAllowedLanguages turns a metadata profile's allowed_languages CSV
// (e.g. "eng,fre,ger") into the normalized lowercase set used when filtering
// metadata responses. Whitespace around codes is tolerated. An empty string
// or a single "any" entry returns nil — callers treat nil as "don't filter".
func ParseAllowedLanguages(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(csv, ",") {
		code := strings.ToLower(strings.TrimSpace(part))
		if code == "" {
			continue
		}
		if code == "any" {
			return nil
		}
		out = append(out, code)
	}
	return out
}

// The four tables below are the only ISO 639 tables in the codebase, and
// NormalizeLanguageCode is the only thing that reads them. (languageNames,
// further down, is a display table keyed by the codes they produce.) There
// used to be four competing copies of these (here, in the indexer's release
// filter, and twice over in the Audible ingestion paths) and they disagreed
// with each other, so the same
// language filtered differently depending on which code path happened to be
// consulted. Add a language here and every caller learns it at once.

// iso639TwoLetterToB maps ISO 639-1 two-letter codes to the ISO 639-2/B
// three-letter vocabulary Bindery stores in Book.Language and metadata-profile
// allowed_languages. Anything not listed passes through unchanged so a rarer
// language still round-trips rather than being silently dropped.
var iso639TwoLetterToB = map[string]string{
	"en": "eng", "fr": "fre", "de": "ger", "nl": "dut", "es": "spa",
	"it": "ita", "pt": "por", "ja": "jpn", "zh": "chi", "ru": "rus",
	"sv": "swe", "no": "nor", "da": "dan", "pl": "pol", "cs": "cze",
	"tr": "tur", "hi": "hin", "ko": "kor", "ar": "ara", "fi": "fin",
	"el": "gre", "hu": "hun", "ro": "rum", "ca": "cat", "la": "lat",
	// Bokmål and Nynorsk are the two written forms of Norwegian. Folded onto
	// the macrolanguage, see iso639IndividualToMacro.
	"nb": "nor", "nn": "nor",
}

// iso639IndividualToMacro folds an ISO 639-3 individual language onto the ISO
// 639-2 macrolanguage Bindery stores, where both are in real use for the same
// books. A Norwegian EPUB or catalogue record is often tagged "nob" (Bokmål)
// or "nno" (Nynorsk) rather than "nor", while the profile editor offers only
// "nor", so without this a profile allowing Norwegian rejected Norwegian
// books and files (#2998).
var iso639IndividualToMacro = map[string]string{
	"nob": "nor", "nno": "nor",
}

// iso639TermToB maps the ISO 639-2/T (terminology) code onto the 639-2/B
// (bibliographic) code for the twenty languages where the two standards
// disagree. Both spellings are legal ISO 639-2 and providers emit either, but
// Bindery stores and filters on the /B form, so /T has to fold onto it or a
// profile allowing "ger" rejects every book a provider reported as "deu".
// This is a closed set: ISO 639-2 defines exactly these twenty pairs.
var iso639TermToB = map[string]string{
	"sqi": "alb", "hye": "arm", "eus": "baq", "bod": "tib", "mya": "bur",
	"ces": "cze", "cym": "wel", "deu": "ger", "ell": "gre", "fas": "per",
	"fra": "fre", "isl": "ice", "kat": "geo", "mkd": "mac", "mri": "mao",
	"msa": "may", "nld": "dut", "ron": "rum", "slk": "slo", "zho": "chi",
}

// iso639NameToB maps a language written out as a word onto its 639-2/B code.
// Audible reports languages this way ("english", "german"), and release names
// carry them in English and in the language's own spelling.
//
// Only genuine names of languages belong here. The release filter also
// recognises words that merely imply a language (the local word for
// "audiobook", say), but those are evidence read off a release title rather
// than a language a provider reported, so they stay in releaseLanguageTags in
// internal/indexer/searcher.go.
var iso639NameToB = map[string]string{
	"english": "eng",
	"french":  "fre", "francais": "fre", "français": "fre",
	"german": "ger", "deutsch": "ger",
	"spanish": "spa", "espanol": "spa", "español": "spa",
	"italian": "ita", "italiano": "ita",
	"dutch": "dut", "nederlands": "dut",
	"portuguese": "por", "portugues": "por", "português": "por",
	"japanese": "jpn",
	"russian":  "rus",
	"chinese":  "chi", "mandarin": "chi",
	"danish": "dan", "dansk": "dan",
	"swedish": "swe", "svenska": "swe",
	"norwegian": "nor", "norsk": "nor",
	"polish": "pol", "polski": "pol",
	"finnish": "fin", "suomi": "fin",
	"hindi":   "hin",
	"turkish": "tur", "turkce": "tur", "türkçe": "tur",
	"arabic": "ara",
	"korean": "kor",
	"czech":  "cze", "cestina": "cze", "čeština": "cze",
	"greek": "gre", "ellinika": "gre",
	"hungarian": "hun", "magyar": "hun",
	"romanian": "rum", "romana": "rum", "română": "rum",
	"catalan": "cat", "catala": "cat", "català": "cat",
	"latin": "lat",
}

// NormalizeLanguageCode canonicalizes a language code from any source (an
// EPUB's dc:language, provider metadata, a hand-edited metadata profile) into
// the lowercased ISO 639-2/B form the language filter compares against. It
// resolves a written-out language name ("German", "Deutsch"), drops a region
// or script subtag ("en-US" to "en" to "eng", "zh-Hans" to "chi"), maps a
// known two-letter code to its three-letter equivalent, folds ISO 639-2/T onto
// 639-2/B ("deu" to "ger"), and passes anything unrecognised through
// lowercased so it still round-trips. Empty in, empty out.
func NormalizeLanguageCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return ""
	}
	// Names are matched whole. A name is not a code carrying a subtag, so the
	// stripping below must not run before this lookup.
	if b, ok := iso639NameToB[code]; ok {
		return b
	}
	// Drop a region/script subtag: "en-US", "pt_BR", "zh-Hans".
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	if len(code) == 2 {
		if b, ok := iso639TwoLetterToB[code]; ok {
			return b
		}
		return code
	}
	if b, ok := iso639TermToB[code]; ok {
		return b
	}
	if m, ok := iso639IndividualToMacro[code]; ok {
		return m
	}
	return code
}

// languageNames gives the English name of each language Bindery recognises,
// keyed by the ISO 639-2/B code NormalizeLanguageCode produces, so a message
// can say "Swedish" rather than "swe".
var languageNames = map[string]string{
	"alb": "Albanian", "ara": "Arabic", "arm": "Armenian", "baq": "Basque",
	"bur": "Burmese", "cat": "Catalan", "chi": "Chinese", "cze": "Czech",
	"dan": "Danish", "dut": "Dutch", "eng": "English", "fin": "Finnish",
	"fre": "French", "geo": "Georgian", "ger": "German", "gre": "Greek",
	"hin": "Hindi", "hun": "Hungarian", "ice": "Icelandic", "ita": "Italian",
	"jpn": "Japanese", "kor": "Korean", "lat": "Latin", "mac": "Macedonian",
	"mao": "Maori", "may": "Malay", "nor": "Norwegian", "per": "Persian",
	"pol": "Polish", "por": "Portuguese", "rum": "Romanian", "rus": "Russian",
	"slo": "Slovak", "spa": "Spanish", "swe": "Swedish", "tib": "Tibetan",
	"tur": "Turkish", "wel": "Welsh",
}

// LanguageName returns the English name of a language code in any spelling
// NormalizeLanguageCode accepts ("sv", "swe", "sv-SE" all give "Swedish"). A
// code with no known name is returned normalised, so the result is never
// empty for a non-empty input.
func LanguageName(code string) string {
	n := NormalizeLanguageCode(code)
	if name, ok := languageNames[n]; ok {
		return name
	}
	return n
}

// LanguageCodeVariants returns the provider-facing ISO spellings that are
// equivalent to any code in codes. The result includes ISO 639-1, 639-2/B,
// and 639-2/T variants where Bindery knows them. Metadata providers that can
// filter remotely should send all variants: a profile stores the /B form,
// while an upstream catalogue may expose the same language as /T or 639-1.
func LanguageCodeVariants(codes []string) []string {
	seen := make(map[string]struct{})
	add := func(code string) {
		code = strings.ToLower(strings.TrimSpace(code))
		if code != "" {
			seen[code] = struct{}{}
		}
	}
	for _, code := range codes {
		normalized := NormalizeLanguageCode(code)
		add(code)
		add(normalized)
		for two, bibliographic := range iso639TwoLetterToB {
			if bibliographic == normalized {
				add(two)
			}
		}
		for terminology, bibliographic := range iso639TermToB {
			if bibliographic == normalized {
				add(terminology)
			}
		}
		for individual, macro := range iso639IndividualToMacro {
			if macro == normalized {
				add(individual)
			}
		}
	}
	variants := make([]string, 0, len(seen))
	for code := range seen {
		variants = append(variants, code)
	}
	slices.Sort(variants)
	return variants
}

// IsLanguageAllowed reports whether code passes the allowed-language filter.
// When allowed is empty the filter is disabled and everything passes. When
// code is empty (source didn't report a language — common with OpenLibrary
// work-level data), unknownFail controls behavior: false keeps the book,
// true rejects it. See issue #232.
//
// Both the incoming code and the allowed entries are run through
// NormalizeLanguageCode before comparing: providers hand us whatever
// vocabulary they use (Google Books returns ISO 639-1 "en"/"en-US"), and a
// profile allowing "eng" must not reject those spellings of the same
// language. See issue #1729.
func IsLanguageAllowed(code string, allowed []string, unknownFail bool) bool {
	if len(allowed) == 0 {
		return true
	}
	code = NormalizeLanguageCode(code)
	if code == "" {
		return !unknownFail
	}
	return slices.ContainsFunc(allowed, func(a string) bool {
		return NormalizeLanguageCode(a) == code
	})
}
