package models

import "strings"

// Edition types (#1358). The empty string is "unknown" and is what every
// existing row holds. A parser that cannot tell returns it rather than guess.
const (
	EditionTypeUnabridged = "unabridged"
	EditionTypeAbridged   = "abridged"
	EditionTypeDramatized = "dramatized"
	EditionTypeFullCast   = "full_cast"
)

// Quality profile edition preferences. Empty is off: ranking is exactly what it
// was before the setting existed.
const (
	EditionPrefPreferUnabridged  = "prefer_unabridged"
	EditionPrefRequireUnabridged = "require_unabridged"
)

// NormalizeEditionType returns s when it is a known edition type, else "".
func NormalizeEditionType(s string) string {
	switch t := strings.ToLower(strings.TrimSpace(s)); t {
	case EditionTypeUnabridged, EditionTypeAbridged, EditionTypeDramatized, EditionTypeFullCast:
		return t
	}
	return ""
}

// NormalizeEditionPreference returns s when it is a known preference, else "".
func NormalizeEditionPreference(s string) string {
	switch t := strings.ToLower(strings.TrimSpace(s)); t {
	case EditionPrefPreferUnabridged, EditionPrefRequireUnabridged:
		return t
	}
	return ""
}

// CleanNarrators trims, drops empties and removes case-insensitive duplicates
// while keeping order. It never returns nil.
func CleanNarrators(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, n := range in {
		n = strings.Join(strings.Fields(n), " ")
		if n == "" {
			continue
		}
		k := strings.ToLower(n)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
}

// SplitNarrators splits the single narrator string books carry ("A, B",
// "A & B", "A and B", "A; B") into a list. It is a read fallback only and is
// never written back over a stored list.
func SplitNarrators(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{}
	}
	r := strings.NewReplacer(";", ",", " & ", ",", " and ", ",", " And ", ",", " AND ", ",")
	return CleanNarrators(strings.Split(r.Replace(s), ","))
}

// EffectiveNarrators is the edition's stored list when it has one, otherwise
// the book's narrator string split into names.
func EffectiveNarrators(stored []string, bookNarrator string) []string {
	if c := CleanNarrators(stored); len(c) > 0 {
		return c
	}
	return SplitNarrators(bookNarrator)
}
