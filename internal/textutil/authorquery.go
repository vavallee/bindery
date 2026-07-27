package textutil

import (
	"strings"
	"unicode"
)

// The helpers below were byte-identical copies in internal/abs
// (import_author_matcher.go) and internal/api (authors.go), differing only in
// which dedupe wrapper they called. Two copies of a matching rule is how the
// rules drift, which is the whole subject of #1648 — so they live here now and
// both packages delegate.

// SortName converts a display name to "Last, First Middle" sort form, leaving
// single-token names untouched. It was copied into internal/api,
// internal/abs, openlibrary and hardcover; all four were identical.
//
// Deliberately naive: it flips on the last whitespace-separated token and does
// no particle handling ("van", "de", "von") or script awareness. That is fine
// for a DISPLAY and ordering value — do not compare identities with it. Author
// identity comparison is MatchAuthorName; ordering is db.authorSortKey.
func SortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}

// AuthorSearchQueries expands an author name into the provider query strings
// worth trying, most specific first: the name verbatim, a compact-initials form
// ("J.R.R. Tolkien" from "J. R. R. Tolkien"), the normalized form, and — when
// everything but the last token is a single letter — the bare surname.
// Duplicates are removed case-insensitively, preserving order.
func AuthorSearchQueries(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	queries := []string{name}
	if compact := CompactInitialsAuthorQuery(name); compact != "" {
		queries = append(queries, compact)
	}
	if norm := NormalizeAuthorName(name); norm != "" {
		queries = append(queries, norm)
		if surname := InitialedSurnameFallback(norm); surname != "" {
			queries = append(queries, surname)
		}
	}
	return dedupeFold(queries)
}

// CompactInitialsAuthorQuery collapses a leading run of two or more single-
// letter tokens into the compact form providers often index under:
// "George R. R. Martin" → "G.R.R. Martin" is NOT produced (the run must start
// at the first token), but "J. R. R. Tolkien" → "J.R.R. Tolkien" is. Returns
// "" when the name has no such run.
func CompactInitialsAuthorQuery(name string) string {
	fields := strings.Fields(name)
	if len(fields) < 3 {
		return ""
	}
	initials := make([]string, 0, len(fields)-1)
	idx := 0
	for idx < len(fields)-1 {
		initial, ok := AuthorInitial(fields[idx])
		if !ok {
			break
		}
		initials = append(initials, strings.ToUpper(initial)+".")
		idx++
	}
	if len(initials) < 2 || idx >= len(fields) {
		return ""
	}
	return strings.Join(initials, "") + " " + strings.Join(fields[idx:], " ")
}

// AuthorInitial reports whether token carries exactly one letter or digit, and
// returns it lowercased. "R." and "R" are initials; "Le" and "" are not.
func AuthorInitial(token string) (string, bool) {
	var letters []rune
	for _, r := range token {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters = append(letters, unicode.ToLower(r))
		}
	}
	if len(letters) != 1 {
		return "", false
	}
	return string(letters[0]), true
}

// InitialedSurnameFallback returns the last token of an already-normalized name
// when every preceding token is a single rune, so "j r r tolkien" yields
// "tolkien" and "george r r martin" yields "". Providers that index only the
// surname need this; anything with a real forename does not.
func InitialedSurnameFallback(normalized string) string {
	fields := strings.Fields(normalized)
	if len(fields) < 2 {
		return ""
	}
	for _, field := range fields[:len(fields)-1] {
		if len([]rune(field)) != 1 {
			return ""
		}
	}
	return fields[len(fields)-1]
}

// dedupeFold removes blanks and case-insensitive duplicates while preserving
// first-seen order and the original casing.
func dedupeFold(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}
