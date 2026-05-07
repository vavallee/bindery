// Package isbnutil normalizes ISBN inputs for metadata provider lookups.
package isbnutil

import (
	"strings"
	"unicode"
)

// Normalize strips common ISBN separators and uppercases ISBN-10 check digits.
// It intentionally leaves other characters alone so invalid inputs still fail
// at the provider instead of being silently rewritten into a different value.
func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == 'X':
			b.WriteByte('X')
		case r == '-' || r == '_' || unicode.IsSpace(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	normalized := b.String()
	if idx := strings.IndexByte(normalized, 'X'); idx >= 0 && idx != len(normalized)-1 {
		normalized = strings.ReplaceAll(normalized, "X", "")
	}
	return normalized
}
