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

// sortNameParticles encode the LC-PCC MGD "Access Point for Person"
// (2025-03-04) policy, reduced to the one question this function can answer
// from the string alone: does the particle travel with the surname or with the
// forename?
//
// trailing: the name files under the following word and the particle goes to
// the end. Dutch "van", German "von" and "zu", Portuguese "da" and "dos",
// Spanish "de" and "del". "Johann Wolfgang von Goethe" files as "Goethe,
// Johann Wolfgang von".
//
// leading: the particle stays attached to the surname whatever its case,
// because the language files it that way. French "Le", "La", "Du", "Des";
// Spanish "El", "Los"; and the patronymics "Mac", "Mc", "O'", "Fitz", "St.".
// "Ursula K. Le Guin" files as "Le Guin, Ursula K.", never "Guin, Ursula K. Le".
var (
	trailingParticles = map[string]bool{
		"von": true, "zu": true, "van": true, "der": true, "den": true,
		"ter": true, "ten": true, "de": true, "del": true, "da": true,
		"das": true, "dos": true, "do": true, "d'": true, "af": true, "av": true,
	}
	leadingParticles = map[string]bool{
		"le": true, "la": true, "les": true, "du": true, "des": true,
		"el": true, "los": true, "las": true, "ver": true, "mac": true,
		"mc": true, "o'": true, "fitz": true, "st": true, "saint": true,
	}
	// A generational suffix belongs to neither, and follows the forename:
	// "Martin Luther King Jr." files as "King, Martin Luther Jr.".
	generationalSuffixes = map[string]bool{
		"jr": true, "sr": true, "ii": true, "iii": true, "iv": true,
	}
)

// SortName converts a display name to "Last, First Middle" sort form, leaving
// single-token names untouched. Nine packages delegate here rather than keep a
// copy: internal/api, internal/abs, openlibrary and hardcover from the first
// consolidation, then internal/calibre, internal/hardcoverlistsyncer,
// internal/importer, internal/migrate and googlebooks (#2363). Keep it that
// way. importer/renamer.go's authorSortName feeds the {SortAuthor} naming
// token, so a change here renames folders on disk, and a divergent copy means
// the library layout and the UI disagree about the same author.
//
// It is a DISPLAY and ordering value. Do not compare identities with it:
// author identity is MatchAuthorName, ordering is db.authorSortKey.
//
// Three kinds of name are returned untouched, because inverting them loses:
// one already carrying a comma (someone has inverted it, and guessing again
// destroys the answer), a single token, and an all-CJK name, whose authorised
// form is surname-first as written.
//
// Where the tables cannot decide, case does. That is the BibTeX "von part"
// rule, and it reproduces the LC outcomes without needing to know the
// language: a lowercase mid-name particle trails ("Vincent van Gogh" to
// "Gogh, Vincent van") and a capitalised one leads ("Thomas De Quincey" to
// "De Quincey, Thomas"). It is a heuristic, and it is wrong for anyone who
// writes their own particle against their language's convention. The
// alternative is knowing the language of every name, which we do not.
func SortName(name string) string {
	s := strings.TrimSpace(name)
	if s == "" || strings.Contains(s, ",") {
		return name
	}
	parts := strings.Fields(s)
	if len(parts) < 2 || isAllCJK(s) {
		return name
	}

	suffix := ""
	if len(parts) > 2 && generationalSuffixes[particleKey(parts[len(parts)-1])] {
		suffix = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		return name
	}

	// Find the contiguous run of particles sitting immediately before the last
	// token. Index 0 is never a particle: a name beginning with one has no
	// forename to inherit it.
	runStart := len(parts) - 1
	for i := len(parts) - 2; i >= 1; i-- {
		k := particleKey(parts[i])
		if !trailingParticles[k] && !leadingParticles[k] {
			break
		}
		runStart = i
	}

	// The first token of the run decides for the whole run, which is what makes
	// a compound particle work. Spanish "de la" trails as a unit ("Jose de la
	// Cruz" to "Cruz, Jose de la"), and reading "la" on its own would file it
	// under L.
	surnameStart := len(parts) - 1
	if runStart < len(parts)-1 {
		first := parts[runStart]
		k := particleKey(first)
		switch {
		case leadingParticles[k]:
			surnameStart = runStart
		case trailingParticles[k] && !startsUpper(first):
			// Whole run trails.
		default:
			surnameStart = runStart
		}
	}

	forename := append([]string{}, parts[:runStart]...)
	forename = append(forename, parts[runStart:surnameStart]...)
	if suffix != "" {
		forename = append(forename, suffix)
	}
	if len(forename) == 0 {
		return name
	}
	surname := strings.Join(parts[surnameStart:], " ")
	// A particle that now begins the access point is capitalised, which is what
	// LC-PCC prints: "Daphne du Maurier" files as "Du Maurier, Daphne".
	if surnameStart < len(parts)-1 && !startsUpper(parts[surnameStart]) {
		surname = capitaliseFirst(surname)
	}
	return surname + ", " + strings.Join(forename, " ")
}

// particleKey reduces a token to its table form: lowercased, with a trailing
// full stop dropped so "St." and "St" agree.
func particleKey(tok string) string {
	return strings.TrimSuffix(strings.ToLower(tok), ".")
}

func startsUpper(tok string) bool {
	for _, r := range tok {
		return unicode.IsUpper(r)
	}
	return false
}

// isAllCJK reports whether s has letters and every one of them is Han,
// Hiragana, Katakana or Hangul.
func isAllCJK(s string) bool {
	sawLetter := false
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		sawLetter = true
		if !unicode.Is(unicode.Han, r) && !unicode.Is(unicode.Hiragana, r) &&
			!unicode.Is(unicode.Katakana, r) && !unicode.Is(unicode.Hangul, r) {
			return false
		}
	}
	return sawLetter
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

// capitaliseFirst upper-cases the first rune of s, leaving the rest alone.
func capitaliseFirst(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+len(string(r)):]
	}
	return s
}
