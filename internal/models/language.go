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

// IsLanguageAllowed returns true if code matches any entry in allowed, or if
// allowed is empty (filter disabled). When allowed is non-empty and code is
// empty (source didn't report a language), the book is rejected — failing
// closed prevents non-English editions from slipping through when OL omits
// language data at the work level.
func IsLanguageAllowed(code string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return false
	}
	return slices.Contains(allowed, code)
}
