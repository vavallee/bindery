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

// The three tables below are the only ISO 639 tables in the codebase, and
// NormalizeLanguageCode is the only thing that reads them. There used to be
// four such tables (here, in the indexer's release filter, and twice over in
// the Audible ingestion paths) and they disagreed with each other, so the same
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
	return code
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
