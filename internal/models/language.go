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

// iso639TwoLetterToB maps common ISO 639-1 two-letter codes to the ISO 639-2/B
// three-letter vocabulary Bindery stores in Book.Language and metadata-profile
// allowed_languages. Deliberately scoped to the languages users actually filter
// on; anything not listed passes through unchanged so a rarer language still
// round-trips rather than being silently dropped. Mirrors the indexer's
// release-tag alias table (internal/indexer/searcher.go).
var iso639TwoLetterToB = map[string]string{
	"en": "eng", "fr": "fre", "de": "ger", "nl": "dut", "es": "spa",
	"it": "ita", "pt": "por", "ja": "jpn", "zh": "chi", "ru": "rus",
	"sv": "swe", "no": "nor", "da": "dan", "pl": "pol", "cs": "cze",
	"tr": "tur", "hi": "hin", "ko": "kor", "ar": "ara",
}

// NormalizeLanguageCode canonicalizes a language code from any source (an EPUB's
// dc:language, provider metadata) into the lowercased ISO 639-2/B form the
// language filter compares against. It drops a region/script subtag
// ("en-US" → "en" → "eng", "zh-Hans" → "chi"), maps known two-letter codes to
// their three-letter equivalent, and passes anything already three-letter (or
// unrecognised) through lowercased so it still round-trips. Empty in, empty out.
func NormalizeLanguageCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return ""
	}
	// Drop a region/script subtag: "en-US", "pt_BR", "zh-Hans".
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	if len(code) == 2 {
		if b, ok := iso639TwoLetterToB[code]; ok {
			return b
		}
	}
	return code
}

// IsLanguageAllowed reports whether code passes the allowed-language filter.
// When allowed is empty the filter is disabled and everything passes. When
// code is empty (source didn't report a language — common with OpenLibrary
// work-level data), unknownFail controls behavior: false keeps the book,
// true rejects it. See issue #232.
func IsLanguageAllowed(code string, allowed []string, unknownFail bool) bool {
	if len(allowed) == 0 {
		return true
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return !unknownFail
	}
	return slices.Contains(allowed, code)
}
