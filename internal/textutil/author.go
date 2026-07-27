// Package textutil contains small normalization and cleanup helpers used across imports and API responses.
package textutil

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeAuthorName lower-cases the name, strips punctuation/diacritics-adjacent
// characters, and collapses whitespace. Returned form is suitable for key-style
// equality comparisons but still preserves token spacing.
func NormalizeAuthorName(name string) string {
	name = norm.NFD.String(strings.TrimSpace(name))
	if name == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(name))
	spacePending := false
	for _, r := range name {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if spacePending && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(unicode.ToLower(r))
			spacePending = false
		default:
			spacePending = true
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// authorSuffixes is the allow-list of trailing generational/numeric suffixes
// that NormalizeAuthorNameWithVariants will drop.
var authorSuffixes = map[string]struct{}{
	"jr":  {},
	"sr":  {},
	"ii":  {},
	"iii": {},
	"iv":  {},
	"v":   {},
}

// stripAuthorSuffixes removes any trailing tokens that look like Jr/Sr/II/III/IV/V
// so that "John Smith Jr." compares equal to "John Smith". Single-letter
// initials are preserved even if "v" matches, as long as other tokens exist.
func stripAuthorSuffixes(tokens []string) []string {
	for len(tokens) > 1 {
		last := tokens[len(tokens)-1]
		if _, ok := authorSuffixes[last]; !ok {
			break
		}
		tokens = tokens[:len(tokens)-1]
	}
	return tokens
}

// compactInitials collapses runs of single-letter tokens into a single
// concatenated token: {"r","r","haywood"} -> {"rr","haywood"}. Leaves non-initial
// tokens untouched.
func compactInitials(tokens []string) []string {
	if len(tokens) == 0 {
		return tokens
	}
	out := make([]string, 0, len(tokens))
	var buf strings.Builder
	for _, tok := range tokens {
		if len(tok) == 1 {
			buf.WriteString(tok)
			continue
		}
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
		out = append(out, tok)
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// expandInitials splits a compact-initials token back into single-letter
// tokens: {"rr","haywood"} -> {"r","r","haywood"}. Applies only when a token
// is all letters and <=3 characters long and not the final token; this
// prevents splitting real short surnames like "Wu".
func expandInitials(tokens []string) []string {
	if len(tokens) < 2 {
		return tokens
	}
	out := make([]string, 0, len(tokens)+2)
	for idx, tok := range tokens {
		if idx < len(tokens)-1 && len(tok) >= 2 && len(tok) <= 3 && allLower(tok) {
			for _, r := range tok {
				out = append(out, string(r))
			}
			continue
		}
		out = append(out, tok)
	}
	return out
}

func allLower(s string) bool {
	for _, r := range s {
		if !unicode.IsLower(r) {
			return false
		}
	}
	return s != ""
}

// lastFirstSwap returns the "last first" reordering of tokens, or nil if the
// swap would be a no-op (e.g. single-token names).
func lastFirstSwap(tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}
	out := make([]string, 0, len(tokens))
	out = append(out, tokens[len(tokens)-1])
	out = append(out, tokens[:len(tokens)-1]...)
	return out
}

// NormalizeAuthorNameWithVariants returns a de-duplicated list of normalized
// forms of the author name, suitable for equality-style comparisons:
//   - base normalized ("r r haywood")
//   - suffix-stripped ("john smith" from "John Smith Jr.")
//   - compact-initials ("rr haywood")
//   - expanded-initials ("r r haywood" from "rr haywood")
//   - last-first ("haywood r r")
//   - ASCII-transliterated ("joerg mueller" from "Jörg Müller")
//
// Callers should treat any match across the two variant sets as equivalent.
// The first element is always the canonical base form.
//
// The ASCII-transliterated chain exists because NormalizeAuthorName STRIPS
// diacritics (ö→o) while every title normalizer in the tree EXPANDS them
// (ö→oe), so "Jörg Müller" and the ASCII-ised "Joerg Mueller" that German
// library folders and providers routinely carry scored 0.9347 — just under the
// 0.94 auto threshold, landing in the ambiguous band that either floods the
// review queue or creates a duplicate author row (#1647). Adding the expanded
// form as a variant makes that pair Exact. Variants only ever ADD matches, so
// no previously-matching pair can stop matching.
func NormalizeAuthorNameWithVariants(name string) []string {
	base := NormalizeAuthorName(name)
	if base == "" {
		return nil
	}

	seen := make(map[string]struct{}, 6)
	out := make([]string, 0, 6)
	add := func(toks []string) {
		if len(toks) == 0 {
			return
		}
		v := strings.Join(toks, " ")
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	addForm := func(raw string) {
		tokens := strings.Fields(NormalizeAuthorName(raw))
		tokens = stripAuthorSuffixes(tokens)
		if len(tokens) == 0 {
			return
		}
		add(tokens)
		add(compactInitials(tokens))
		add(expandInitials(tokens))
		add(lastFirstSwap(tokens))
		add(compactInitials(lastFirstSwap(tokens)))
		add(expandInitials(lastFirstSwap(tokens)))
	}
	addName := func(raw string) {
		addForm(raw)
		// Only pay for the second chain when the name actually contains
		// something to transliterate — pure-ASCII names are the common case and
		// would just re-derive identical variants that `seen` discards anyway.
		if ascii := asciiTransliterate(raw); ascii != "" {
			addForm(ascii)
		}
	}

	addName(name)
	if before, after, ok := strings.Cut(name, ","); ok {
		addName(strings.TrimSpace(after) + " " + strings.TrimSpace(before))
	}
	return out
}

// asciiTransliterate returns the lowercased name with German umlauts expanded
// (ö→oe) and the non-decomposable Latin letters folded (ø→o, ł→l), or "" when
// neither step changed anything. See NormalizeAuthorNameWithVariants for why
// this second romanisation is needed alongside the diacritic-stripping one.
//
// "Nesbø"/"Nesbo" and "Łukasz"/"Lukasz" are the same class as the umlaut case:
// NFD leaves those letters intact, so stripping combining marks alone never
// reconciles them with the ASCII spelling a provider or folder name used.
func asciiTransliterate(name string) string {
	lower := strings.ToLower(name)
	folded := FoldNonDecomposableLatin(TransliterateUmlauts(lower))
	if folded == lower {
		return ""
	}
	return folded
}

// AuthorMatchKind classifies how confident a name match is.
type AuthorMatchKind int

const (
	// AuthorMatchNone means no variant pairing was close enough to consider.
	AuthorMatchNone AuthorMatchKind = iota
	// AuthorMatchExact means a normalized variant of each side compared equal.
	AuthorMatchExact
	// AuthorMatchFuzzyAuto means the best Jaro-Winkler score across variants
	// cleared the auto-accept threshold.
	AuthorMatchFuzzyAuto
	// AuthorMatchFuzzyAmbiguous means the best score was close but below the
	// auto threshold; the caller should surface a review rather than silently
	// merging.
	AuthorMatchFuzzyAmbiguous
)

// Jaro-Winkler thresholds for author-name matching. Keep conservative so we do
// not silently merge distinct authors.
const (
	AuthorMatchAutoThreshold    = 0.94
	AuthorMatchAmbiguousMinimum = 0.88
)

// AuthorMatchResult is the outcome of comparing two author names across all
// supported variants.
type AuthorMatchResult struct {
	Kind  AuthorMatchKind
	Score float64 // best Jaro-Winkler score observed (0 when Kind is None)
}

// MatchAuthorName compares two raw author names (no prior normalization
// required) and reports the strongest class of match. Exact-via-variants beats
// fuzzy; ambiguous-fuzzy never auto-matches.
func MatchAuthorName(a, b string) AuthorMatchResult {
	av := NormalizeAuthorNameWithVariants(a)
	bv := NormalizeAuthorNameWithVariants(b)
	if len(av) == 0 || len(bv) == 0 {
		return AuthorMatchResult{Kind: AuthorMatchNone}
	}

	for _, x := range av {
		for _, y := range bv {
			if x == y {
				return AuthorMatchResult{Kind: AuthorMatchExact, Score: 1}
			}
		}
	}

	best := 0.0
	for _, x := range av {
		for _, y := range bv {
			score := JaroWinkler(x, y)
			if score > best {
				best = score
			}
		}
	}
	switch {
	case best >= AuthorMatchAutoThreshold:
		return AuthorMatchResult{Kind: AuthorMatchFuzzyAuto, Score: best}
	case best >= AuthorMatchAmbiguousMinimum:
		return AuthorMatchResult{Kind: AuthorMatchFuzzyAmbiguous, Score: best}
	default:
		return AuthorMatchResult{Kind: AuthorMatchNone, Score: best}
	}
}
