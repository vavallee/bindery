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
//
// The decomposition is NFKD rather than NFD, so the compatibility forms fold
// too. Providers and scraped catalogues carry full-width Latin ("Ｍｕｒａｋａｍｉ")
// and typographic ligatures ("ﬁ"), which NFD leaves alone and which therefore
// used to key as a different author from the ordinary spelling. NFKD is lossy,
// but this is a comparison key and never a displayed value, which is the
// condition UAX #15 attaches to using a compatibility form at all.
//
// Marks are dropped only on a Latin or Greek base, through the same
// stripLatinGreekMarks that FoldForSlug and FoldForSearch use. This function
// predates that helper and dropped every Mn unconditionally, which is right for
// an acute on an e and wrong for kana: the dakuten and handakuten change the
// letter rather than decorate it, so ズ keyed as ス, がっこう as かっこう and
// ヴィクトル as ウィクトル (#2452). This is the identity alphabet, used at
// nineteen call sites to decide whether two records are the same person, so a
// collision here merges two authors.
//
// The NFC after the NFKD is what makes that judgement possible. NFKD leaves a
// kana voicing mark standing on its own, where it has no script of its own to
// be judged by; recomposing puts it back on its base so it is seen as part of
// ズ. The output is not persisted anywhere, so nothing needs migrating, but it
// does change which authors compare equal.
func NormalizeAuthorName(name string) string {
	name = norm.NFC.String(norm.NFKD.String(strings.TrimSpace(name)))
	if name == "" {
		return ""
	}
	name = stripLatinGreekMarks(strings.ToLower(name))

	var b strings.Builder
	b.Grow(len(name))
	spacePending := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if spacePending && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(unicode.ToLower(r))
			spacePending = false
		case unicode.Is(unicode.Mn, r) && !spacePending && b.Len() > 0:
			// A mark stripLatinGreekMarks chose to keep, sitting directly on
			// the letter just written, so it belongs to a script where the mark
			// is part of that letter. It joins the word rather than breaking
			// it, which is what the default arm would do.
			b.WriteRune(r)
		case unicode.Is(unicode.Mn, r):
			// The same kind of mark, but floating: it follows a separator or
			// opens the string, so there is no letter for it to belong to.
			// Writing it here would swallow the pending boundary and glue two
			// name tokens into one ("Tanaka ゙Suzuki" keyed as one word), which
			// is the opposite of what #2452 is for. Dropped, and the boundary
			// is kept.
			continue
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
	return authorNameForms(name, true)
}

// authorNameForms is NormalizeAuthorNameWithVariants with the last-first
// reordering made optional. See authorNameFormSets.
func authorNameForms(name string, includeSwap bool) []string {
	ordered, all := authorNameFormSets(name)
	if includeSwap {
		return all
	}
	return ordered
}

// authorNameFormSets builds both variant sets in one pass over the name.
//
// all is NormalizeAuthorNameWithVariants, byte-for-byte what it always was,
// first element first: callers index into it.
//
// ordered holds only the forms that preserve the order the name was written in
// — plus, for a name written "Last, First", the order its comma explicitly
// declares. MatchAuthorName needs that narrower set to tell "Haywood, R.R."
// from "R.R. Haywood" (a reordering the writer signposted, so it is the same
// name) apart from "Stanley Paul" against "Paul Stanley" (two orderings of two
// ordinary words, which is either one person or two, and the names alone
// cannot say).
//
// The two sets keep separate dedupe tables and neither is a slice of the
// other. They cannot share one: for "Haywood, R.R." the form "r r haywood" is
// first produced as a last-first swap of the written order and only then as
// the comma chain's own base form, so a shared table would have dropped it as
// a duplicate before the chain that legitimately owns it ever ran.
func authorNameFormSets(name string) (ordered, all []string) {
	if NormalizeAuthorName(name) == "" {
		return nil, nil
	}

	seenAll := make(map[string]struct{}, 6)
	seenOrdered := make(map[string]struct{}, 4)
	add := func(toks []string, keepsOrder bool) {
		if len(toks) == 0 {
			return
		}
		v := strings.Join(toks, " ")
		if _, ok := seenAll[v]; !ok {
			seenAll[v] = struct{}{}
			all = append(all, v)
		}
		if !keepsOrder {
			return
		}
		if _, ok := seenOrdered[v]; !ok {
			seenOrdered[v] = struct{}{}
			ordered = append(ordered, v)
		}
	}
	addForm := func(raw string) {
		tokens := strings.Fields(NormalizeAuthorName(raw))
		tokens = stripAuthorSuffixes(tokens)
		if len(tokens) == 0 {
			return
		}
		add(tokens, true)
		add(compactInitials(tokens), true)
		add(expandInitials(tokens), true)
		add(lastFirstSwap(tokens), false)
		add(compactInitials(lastFirstSwap(tokens)), false)
		add(expandInitials(lastFirstSwap(tokens)), false)
	}
	addName := func(raw string) {
		addForm(raw)
		// Only pay for the second chain when the name actually contains
		// something to transliterate — pure-ASCII names are the common case and
		// would just re-derive identical variants that the dedupe discards
		// anyway.
		if ascii := asciiTransliterate(raw); ascii != "" {
			addForm(ascii)
		}
	}

	addName(name)
	if before, after, ok := strings.Cut(name, ","); ok {
		addName(strings.TrimSpace(after) + " " + strings.TrimSpace(before))
	}
	return ordered, all
}

// authorScoringForms are the forms MatchAuthorName weighs field by field
// once no exact pairing exists. They differ from authorNameFormSets' ordered
// forms in two ways (#2881).
//
// No glued run of initials appears here. authorNameFormSets compacts "j r"
// into "jr" so that "J. R. Smith" and "JR Smith" compare equal, which is right
// for the exact tier and wrong for scoring: there "jr" read as a spelled out
// forename beginning with J, which is how "J. R. Smith" against "J. T. Smith"
// scored as a confident match.
//
// And a short word is split into letters only when it reads as initials. When
// the name carries case information (both capitals and small letters), that is
// a word written in capitals ("JRR", "KL", "AJ") or one with no vowel at all
// ("Jrr", "Tj"), which no forename is. "Ann", "Amy" and "Ed" stay whole, so
// they are not taken for a run of initials that conflicts with a middle
// initial on the other side. A dotted group needs no rule, normalisation
// already splits "J.R.R." into letters. A name written in a single case says
// nothing either way, so every short non final word may be initials, as
// before, and both readings are scored.
func authorScoringForms(name string) []string {
	if NormalizeAuthorName(name) == "" {
		return nil
	}
	informative, initials := authorInitialWords(name)
	var out []string
	seen := map[string]struct{}{}
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
		tokens := stripAuthorSuffixes(strings.Fields(NormalizeAuthorName(raw)))
		if len(tokens) == 0 {
			return
		}
		if !informative {
			add(tokens)
			add(expandInitials(tokens))
			return
		}
		add(expandInitialWords(tokens, initials))
	}
	addName := func(raw string) {
		addForm(raw)
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

// authorInitialWords reports whether the raw name carries case information and,
// if it does, which normalized words read as a run of initials: two or three
// letters written in capitals ("JRR", "KL"), or two or three Latin letters with
// no vowel ("Jrr", "Tj"). The transliterated spelling of each such word is
// included, so the ASCII chain sees the same set.
func authorInitialWords(name string) (bool, map[string]struct{}) {
	hasUpper, hasLower := false, false
	for _, r := range name {
		if unicode.IsUpper(r) {
			hasUpper = true
		} else if unicode.IsLower(r) {
			hasLower = true
		}
	}
	if !hasUpper || !hasLower {
		return false, nil
	}
	initials := map[string]struct{}{}
	words := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.Is(unicode.Mn, r)
	})
	for _, w := range words {
		letters := 0
		upper := true
		for _, r := range w {
			if unicode.Is(unicode.Mn, r) {
				continue
			}
			letters++
			if !unicode.IsUpper(r) {
				upper = false
			}
		}
		if letters < 2 || letters > 3 {
			continue
		}
		key := NormalizeAuthorName(w)
		if !upper && !vowelless(key) {
			continue
		}
		initials[key] = struct{}{}
		if ascii := asciiTransliterate(w); ascii != "" {
			initials[NormalizeAuthorName(ascii)] = struct{}{}
		}
	}
	return true, initials
}

// vowelless reports whether s is plain a to z with no vowel (y counts as one),
// the shape of initials run together and never of a forename.
func vowelless(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
		switch r {
		case 'a', 'e', 'i', 'o', 'u', 'y':
			return false
		}
	}
	return true
}

// expandInitialWords splits the non final words that authorInitialWords marked
// as initials into one token per letter, and leaves every other word whole.
func expandInitialWords(tokens []string, initials map[string]struct{}) []string {
	if len(tokens) < 2 || len(initials) == 0 {
		return tokens
	}
	out := make([]string, 0, len(tokens)+2)
	for idx, tok := range tokens {
		if _, ok := initials[tok]; ok && idx < len(tokens)-1 {
			for _, r := range tok {
				out = append(out, string(r))
			}
			continue
		}
		out = append(out, tok)
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
	// AuthorMatchNone means the evidence for and against the two names being
	// one person did not add up to anything worth a caller's attention.
	AuthorMatchNone AuthorMatchKind = iota
	// AuthorMatchExact means a normalized variant of each side compared equal,
	// in an order both names agree on.
	AuthorMatchExact
	// AuthorMatchFuzzyAuto means the per-field evidence cleared the
	// auto-accept weight: safe to merge without asking.
	AuthorMatchFuzzyAuto
	// AuthorMatchFuzzyAmbiguous means the evidence was real but short of the
	// auto weight; the caller should surface a review rather than silently
	// merging.
	AuthorMatchFuzzyAmbiguous
)

// Deprecated: whole-name Jaro-Winkler thresholds. MatchAuthorName no longer
// bands on a single score — see the weight table below — and a Kind is no
// longer recoverable from a Score: a pairing carried by an exact surname plus
// compatible initials is FuzzyAuto at a whole-name score of 0.9040, and one
// carried by a shared forename over a near-miss short surname is refused at
// 0.9750. Nothing in the tree reads them any more; they are kept for one
// release so that an out-of-tree caller still compiles. Compare Kind, or
// Weight against AuthorMatchAutoWeight / AuthorMatchAmbiguousWeight.
const (
	AuthorMatchAutoThreshold    = 0.94
	AuthorMatchAmbiguousMinimum = 0.88
)

// Weight bands. A pairing scores evidence for and against identity field by
// field and the total lands in one of the three fuzzy bands.
const (
	// AuthorMatchAutoWeight is the total evidence needed to merge two names
	// without asking. An exactly-equal surname alone does not reach it.
	AuthorMatchAutoWeight = 5.0
	// AuthorMatchAmbiguousWeight is the total below which the pairing is not
	// worth a human's time either.
	AuthorMatchAmbiguousWeight = 2.0
)

// Per-field weights, in the Fellegi-Sunter shape: each field that can be
// compared contributes evidence, positive or negative, and the fields add.
//
// Every number here was fitted against the pairs in author_test.go rather than
// chosen. The two that carry the design:
//
//   - authorGivenExactWeight (3) and authorSurnameConflictWeight (-3) cancel
//     exactly. "Heinrich Böll" and "Heinrich Mann" share a whole given name
//     and share nothing else, and that has to come out at None, not at a
//     review-queue entry — two people with the same first name are the single
//     most common shape in any library.
//   - authorSurnameShortWeight (-4) is the point of the exercise. Jaro-Winkler
//     scores JW("jones","johnson") = 0.8324 and JW("michelle","michael") =
//     0.9214: on a short surname the prefix bonus rewards a shared first
//     letter far more than the disagreement that follows costs, so no single
//     threshold separates a variant spelling from a different person. Below
//     six runes a near-miss is therefore counted as evidence AGAINST, and a
//     short surname must be equal, not similar, to carry a match.
const (
	authorSurnameExactWeight    = 4.0
	authorSurnameStrongWeight   = 3.0
	authorSurnameCloseWeight    = 1.0
	authorSurnameShortWeight    = -4.0
	authorSurnameDivergeWeight  = -1.0
	authorSurnameConflictWeight = -3.0

	authorGivenExactWeight    = 3.0
	authorGivenInitialWeight  = 1.5
	authorGivenCloseWeight    = 1.5
	authorGivenConflictWeight = -3.0

	// authorExactWeight is what an exact pairing reports: both fields equal.
	authorExactWeight = authorSurnameExactWeight + authorGivenExactWeight
	// authorSwapWeight is the ceiling put on a bare first/last order swap. It
	// sits inside the ambiguous band by construction.
	authorSwapWeight = AuthorMatchAutoWeight - 0.5
)

// Jaro-Winkler cut points for the per-field comparisons.
const (
	authorSurnameStrongJW  = 0.95
	authorSurnameCloseJW   = 0.88
	authorSurnameDivergeJW = 0.75
	authorGivenCloseJW     = 0.94
	authorGivenConflictJW  = 0.80

	// authorShortSurnameRunes is the length at or below which a surname must
	// match exactly. "Jones", "James", "Kelly", "Ross" are all here.
	authorShortSurnameRunes = 5
)

// AuthorMatchResult is the outcome of comparing two author names across all
// supported variants.
type AuthorMatchResult struct {
	Kind AuthorMatchKind
	// Score is the best whole-name Jaro-Winkler score observed across the
	// variant sets (1 when a variant pairing compared equal). It no longer
	// decides Kind and cannot be banded back into one: a pairing carried by an
	// exact surname plus compatible initials is FuzzyAuto at Score 0.9040.
	// Rank with it if you must; gate on Kind or Weight.
	Score float64
	// Weight is the additive per-field evidence total that decided Kind.
	Weight float64
	// DroppedInitial marks an ambiguous pairing whose only difference is that
	// one side leaves out initials the other carries, with an equal surname and
	// nothing that disagrees: "J. Rowling" against "J.K. Rowling", "George
	// Martin" against "George R. R. Martin". It is not enough on its own, but a
	// matching title backs it up (#2881). See ConfirmedByTitle.
	DroppedInitial bool
}

// ConfirmedByTitle reports whether the pairing is good enough for a caller
// that has already matched the title: an auto match, or an ambiguous one that
// only drops initials. A caller deciding on the name alone gates on Kind.
func (r AuthorMatchResult) ConfirmedByTitle() bool {
	switch r.Kind {
	case AuthorMatchExact, AuthorMatchFuzzyAuto:
		return true
	case AuthorMatchFuzzyAmbiguous:
		return r.DroppedInitial
	}
	return false
}

// MatchAuthorName compares two raw author names (no prior normalization
// required) and reports the strongest class of match.
//
// An exact pairing of normalized variants still wins outright. Everything else
// is scored field by field — surname against surname, given name against given
// name — and the total is banded. A single whole-name Jaro-Winkler threshold
// cannot do this job: the score mixes the two fields together and weights them
// by length and shared prefix rather than by how much each one tells you, so
// the threshold that admits "Brandon Sandersen" for "Brandon Sanderson" also
// admits "Christopher Rose" for "Christopher Ross". Winkler's own papers note
// that small threshold changes there produce large accuracy swings, which is a
// description of a decision rule with no stable operating point rather than a
// tuning problem.
//
// Ambiguous never auto-matches; callers surface it for review.
func MatchAuthorName(a, b string) AuthorMatchResult {
	// Order-preserving equality: the same name, spelled or abbreviated
	// differently. This is the tier alias binding and every dedupe path rests
	// on, and it is unchanged. It is also the common case on a rescan, so it is
	// settled before any scoring happens.
	ao, av := authorNameFormSets(a)
	bo, bv := authorNameFormSets(b)
	if len(ao) == 0 || len(bo) == 0 {
		return AuthorMatchResult{Kind: AuthorMatchNone}
	}
	if formsIntersect(ao, bo) {
		return AuthorMatchResult{Kind: AuthorMatchExact, Score: 1, Weight: authorExactWeight}
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

	// Equal only after reordering. When the reordering is signposted — a comma
	// on either side, or an initials group that cannot be a surname — the two
	// names agree on which token is the surname and the pairing is exact.
	// Otherwise "Stanley Paul" and "Paul Stanley" are indistinguishable from
	// two different people who happen to share both words, so cap at ambiguous.
	if formsIntersect(av, bv) {
		if authorOrderIsSignposted(a, b) {
			return AuthorMatchResult{Kind: AuthorMatchExact, Score: 1, Weight: authorExactWeight}
		}
		return AuthorMatchResult{Kind: AuthorMatchFuzzyAmbiguous, Score: best, Weight: authorSwapWeight}
	}

	as, bs := authorScoringForms(a), authorScoringForms(b)
	weight := scoreAuthorFields(as, bs)
	res := AuthorMatchResult{Kind: authorWeightKind(weight), Score: best, Weight: weight}
	if res.Kind == AuthorMatchFuzzyAmbiguous {
		res.DroppedInitial = authorOnlyDropsInitials(as, bs)
	}
	return res
}

// authorOnlyDropsInitials reports whether some pairing of the two sides'
// scoring forms has an equal surname and given names that agree position by
// position, the longer one only adding single letter initials, and no pairing
// with an equal surname has initials that disagree.
func authorOnlyDropsInitials(aForms, bForms []string) bool {
	found := false
	for _, ap := range splitAuthorForms(aForms) {
		for _, bp := range splitAuthorForms(bForms) {
			if ap.surname == "" || ap.surname != bp.surname {
				continue
			}
			switch authorInitialsCompare(ap.given, bp.given) {
			case initialsFirstConflict, initialsMiddleConflict:
				return false
			}
			if givenOnlyDropsInitials(ap.given, bp.given) {
				found = true
			}
		}
	}
	return found
}

// givenOnlyDropsInitials: both given names present, every aligned position
// agrees (equal, or an initial against a word with that first letter), and the
// longer list's extra tokens are all single letters.
func givenOnlyDropsInitials(a, b string) bool {
	at, bt := strings.Fields(a), strings.Fields(b)
	if len(at) == 0 || len(bt) == 0 || len(at) == len(bt) {
		return false
	}
	if len(at) > len(bt) {
		at, bt = bt, at
	}
	for i := range at {
		x, y := []rune(at[i]), []rune(bt[i])
		if len(x) == 1 || len(y) == 1 {
			if x[0] != y[0] {
				return false
			}
			continue
		}
		if at[i] != bt[i] {
			return false
		}
	}
	for _, extra := range bt[len(at):] {
		if len([]rune(extra)) != 1 {
			return false
		}
	}
	return true
}

// authorWeightKind bands an evidence total.
func authorWeightKind(weight float64) AuthorMatchKind {
	switch {
	case weight >= AuthorMatchAutoWeight:
		return AuthorMatchFuzzyAuto
	case weight >= AuthorMatchAmbiguousWeight:
		return AuthorMatchFuzzyAmbiguous
	default:
		return AuthorMatchNone
	}
}

func formsIntersect(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// authorOrderIsSignposted reports whether either raw name says, structurally,
// which of its tokens is the surname: a comma ("Haywood, R.R.") or an initials
// group, which is never a surname ("R.R. Haywood" against "Haywood R R").
func authorOrderIsSignposted(names ...string) bool {
	for _, name := range names {
		if strings.Contains(name, ",") {
			return true
		}
		fields := strings.Fields(NormalizeAuthorName(name))
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields {
			if len([]rune(f)) == 1 {
				return true
			}
		}
	}
	return false
}

// scoreAuthorFields returns the best evidence total across the two sides'
// order-preserving forms. Taking the maximum is what lets the diacritic-
// stripped form of one name meet the ASCII-transliterated form of the other
// ("Jörg Müller" against "Joerg Muller"): the forms are alternative spellings
// of one name, so the strongest pairing is the honest one, and no form
// reorders tokens, so none of them can invent a surname agreement.
func scoreAuthorFields(aForms, bForms []string) float64 {
	aParts := splitAuthorForms(aForms)
	bParts := splitAuthorForms(bForms)
	best := 0.0
	first := true
	for _, ap := range aParts {
		for _, bp := range bParts {
			w := scoreAuthorParts(ap, bp)
			if first || w > best {
				best = w
				first = false
			}
		}
	}
	return best
}

// authorParts is one normalized form split into the two fields that carry
// identity.
type authorParts struct {
	surname string
	given   string
}

// splitAuthorForms splits each form and de-duplicates the results.
func splitAuthorForms(forms []string) []authorParts {
	out := make([]authorParts, 0, len(forms))
	for _, f := range forms {
		p := splitAuthorForm(f)
		if p.surname == "" {
			continue
		}
		dup := false
		for _, seen := range out {
			if seen == p {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, p)
		}
	}
	return out
}

// splitAuthorForm splits an already-normalized name form into surname and
// given name.
//
// The rule is the last whitespace token, which is what SortName does and is
// wrong for particles ("van Gogh") in the same way — deliberately, for now:
// SortName is being made particle-aware separately, and one flip rule that is
// wrong identically on both sides of a comparison still matches a name against
// itself, whereas two different flip rules do not. A name written entirely in
// a non-Latin script is not split at all: CJK names put the family name first
// and are commonly written without a space, so neither the token order nor the
// token count means here what it means in a Latin name.
//
// The comma form is handled before this, by authorNameForms, which emits both
// the written order and the order the comma declares; the highest-scoring
// pairing wins, and for "Goethe, Johann Wolfgang von" that is the one where
// "goethe" is the surname.
func splitAuthorForm(form string) authorParts {
	fields := strings.Fields(form)
	if len(fields) == 0 {
		return authorParts{}
	}
	if len(fields) == 1 {
		return authorParts{surname: fields[0]}
	}
	if !authorFormHasLatin(form) {
		// Non-Latin throughout: the whole name is the comparison unit, spaces
		// and all, so "村上 春樹" and "村上春樹" are one surname either way.
		return authorParts{surname: strings.Join(fields, "")}
	}
	return authorParts{
		surname: fields[len(fields)-1],
		given:   strings.Join(fields[:len(fields)-1], " "),
	}
}

func authorFormHasLatin(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) && unicode.Is(unicode.Latin, r) {
			return true
		}
	}
	return false
}

// scoreAuthorParts adds the surname and given-name evidence for one pairing.
func scoreAuthorParts(a, b authorParts) float64 {
	return scoreAuthorSurname(a.surname, b.surname) + scoreAuthorGiven(a.given, b.given)
}

// scoreAuthorSurname weighs the field that carries almost all of the identity.
func scoreAuthorSurname(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return authorSurnameExactWeight
	}
	aLatin, bLatin := authorFormHasLatin(a), authorFormHasLatin(b)
	if !aLatin || !bLatin {
		// One side is romanised and the other is not. A romanisation is an
		// alias question — somebody has to assert that 刘慈欣 publishes as Liu
		// Cixin — and the character sequences are evidence in neither
		// direction, so they score nothing rather than a penalty.
		return 0
	}
	score := JaroWinkler(a, b)
	shortest := len([]rune(a))
	if l := len([]rune(b)); l < shortest {
		shortest = l
	}
	switch {
	case score >= authorSurnameStrongJW:
		return authorSurnameStrongWeight
	case score >= authorSurnameCloseJW:
		if shortest <= authorShortSurnameRunes {
			return authorSurnameShortWeight
		}
		return authorSurnameCloseWeight
	case score >= authorSurnameDivergeJW:
		return authorSurnameDivergeWeight
	default:
		return authorSurnameConflictWeight
	}
}

// scoreAuthorGiven weighs the given-name field. An absent given name is not
// evidence against: catalogues drop middle names and providers store mononyms.
func scoreAuthorGiven(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return authorGivenExactWeight
	}
	switch authorInitialsCompare(a, b) {
	case initialsCompatible:
		return authorGivenInitialWeight
	case initialsFirstConflict:
		return authorGivenConflictWeight
	case initialsMiddleConflict, initialsOnlyMissing:
		// "A. B. Smith" against "A. C. Smith", or "A. Smith" against
		// "A. B. Smith" (#2881). Name authority practice reads the first as
		// two people and the second as too little to go on. Neither is
		// evidence for the pairing, but neither rules it out: with an equal
		// surname the pair lands in the ambiguous band, where a person can
		// confirm it and nothing claims it automatically. This return also
		// keeps the whole string Jaro-Winkler below from reading
		// "kelly l" and "kelly m" as a typo.
		return 0
	}
	if !authorFormHasLatin(a) || !authorFormHasLatin(b) {
		return 0
	}
	switch score := JaroWinkler(a, b); {
	case score >= authorGivenCloseJW:
		// A typo, not another name: "Micheal" for "Michael" scores 0.9714
		// where "Michelle" against "Michael" only reaches 0.9214.
		return authorGivenCloseWeight
	case score >= authorGivenConflictJW:
		return 0
	default:
		return authorGivenConflictWeight
	}
}

// initialsOutcome is what lining two given names up initial by initial says.
type initialsOutcome int

const (
	// initialsUndecided: the comparison is not about initials (two different
	// spelled out names, or the same forename with more words on one side).
	// The caller falls back to its string similarity.
	initialsUndecided initialsOutcome = iota
	// initialsCompatible: one side abbreviates the other and every aligned
	// position agrees ("j r r" against "john ronald reuel").
	initialsCompatible
	// initialsFirstConflict: the first given name disagrees and at least one
	// side of it is an initial ("j" against "k", "j" against "kevin").
	initialsFirstConflict
	// initialsMiddleConflict: the first given name agrees but a later position
	// pairs an initial with a different letter ("a b" against "a c",
	// "kelly l" against "kelly m").
	initialsMiddleConflict
	// initialsOnlyMissing: every aligned position agrees, but all of that
	// agreement is initial against initial and one side has more given names
	// ("a" against "a b"). A matching initial is weak evidence; a missing one
	// is weaker still.
	initialsOnlyMissing
)

// authorInitialsCompare lines two given-name forms up left to right and
// classifies the result. Positions are compared over the shorter list, because
// dropped middle names are routine in catalogue data.
//
// An initial disagreeing with the letter at the same position is a conflict,
// whatever the rest of the string looks like (#2881). Only when no position
// conflicts does an abbreviation count as compatible, and then only if one was
// actually seen, so two different spelled out names never land here.
func authorInitialsCompare(a, b string) initialsOutcome {
	at, bt := strings.Fields(a), strings.Fields(b)
	if len(at) == 0 || len(bt) == 0 {
		return initialsUndecided
	}
	n := min(len(at), len(bt))
	sawInitial, sawWord, wordMismatch := false, false, false
	for i := range n {
		x, y := []rune(at[i]), []rune(bt[i])
		if len(x) == 1 || len(y) == 1 {
			if x[0] != y[0] {
				switch {
				case i == 0:
					return initialsFirstConflict
				case len(x) != len(y):
					// An initial against a different whole word further in
					// is more often a dropped middle name or a particle
					// shifting the positions ("ursula k le" against
					// "ursula le") than a disagreement, so it is left to the
					// string comparison, as it always was.
					return initialsUndecided
				case wordMismatch || sawInitial:
					// Two initials disagree after a forename that only
					// abbreviates, or already differs: one more strike
					// against, not a near miss.
					return initialsFirstConflict
				}
				return initialsMiddleConflict
			}
			if len(x) != len(y) {
				sawInitial = true
			}
			continue
		}
		sawWord = true
		if at[i] != bt[i] {
			wordMismatch = true
		}
	}
	switch {
	case wordMismatch:
		return initialsUndecided
	case sawInitial:
		return initialsCompatible
	case !sawWord && len(at) != len(bt):
		return initialsOnlyMissing
	}
	return initialsUndecided
}
