package indexer

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/textutil"
)

// NormalizeTitleForDedup returns a canonical form of title used as the
// deduplication key when comparing book rows. The normalization is applied
// symmetrically: both when seeding the "already-seen" set from existing DB
// rows and when keying incoming provider results. This guarantees that two
// rows for the same work which differ only in edition qualifier, whitespace,
// punctuation, Unicode form, or umlaut representation are collapsed to the
// same key.
//
// Steps applied (in order):
//  1. Unicode NFC — composes combining characters into precomposed forms,
//     so "é" (NFD) and "é" (NFC) produce the same key.
//  2. newznab.NormalizeQueryTitle — folds smart quotes to ASCII, strips a
//     trailing parenthesised edition qualifier ("(German Edition)" etc.),
//     and collapses internal whitespace.
//  3. foldPunctuation — deletes apostrophes and turns every other
//     non-alphanumeric into a space (see foldPunctuation for why the two
//     classes must be treated differently).
//  4. strings.ToLower — case-insensitive match.
//  5. TransliterateUmlauts — maps ä→ae, ö→oe, ü→ue, ß→ss so that
//     "Geraeusch" from a release title compares equal to "Geräusch" from
//     the metadata provider.
//
// The key is LOSSLESS with respect to word content: it never truncates. An
// earlier version dropped everything after the first ": " (see #2042), which
// made the key both over- and under-inclusive at the same time — over,
// because "Star Wars: A New Hope" and "Star Wars: The Empire Strikes Back"
// became one key; under, because a provider that omits the colon
// ("Journey of the Pharaohs Numa Files #17") could never meet the form that
// keeps it. Subtitle-only divergence is now handled one level up, by
// CompareTitles, which can say "probably, but corroborate" instead of being
// forced into a yes/no by a lossy string.
func NormalizeTitleForDedup(title string) string {
	title = norm.NFC.String(title)
	title = newznab.NormalizeQueryTitle(title)
	title = foldPunctuation(title)
	title = strings.ToLower(title)
	title = textutil.TransliterateUmlauts(title)
	return title
}

// foldPunctuation removes punctuation disagreements between providers, which
// are routine and carry no meaning. The two character classes must be handled
// differently and getting it backwards is silently wrong:
//
//   - Apostrophes (ASCII ', backtick, and the U+2018/U+2019/U+02BC curly
//     forms) are INTRA-word and are DELETED, so "Poseidon's Arrow" folds onto
//     "Poseidons Arrow". Replacing them with a space instead yields
//     "poseidon s arrow", which matches neither spelling.
//   - Every other non-alphanumeric rune (colon, hyphen, comma, '#', em dash,
//     ampersand, …) is a SEPARATOR and becomes a space. This is what lets
//     "Journey of the Pharaohs: Numa Files #17" and
//     "Journey of the Pharaohs Numa Files #17" produce one key: the colon
//     stops being a distinction rather than becoming a truncation point.
//
// Letters and digits are kept as-is, including non-ASCII ones, so accented
// forms survive to be folded by the NFC and umlaut steps around this call.
// Runs of whitespace are collapsed and the result trimmed.
func foldPunctuation(title string) string {
	var b strings.Builder
	b.Grow(len(title))
	for _, r := range title {
		switch {
		case isApostrophe(r):
			// Deleted, not replaced — see the doc comment.
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isApostrophe(r rune) bool {
	switch r {
	case '\'', '`', '‘', '’', 'ʼ':
		return true
	}
	return false
}

// bracketSuffixRe matches one trailing square-bracketed qualifier. Provider
// titles (Audiobookshelf in particular) routinely append format/edition tags
// this way ("[Unabridged]", "[Dramatized Adaptation]", "[Audiobook]").
// NormalizeTitleForDedup only strips a trailing *parenthesised* qualifier, so
// without this step "The Eye of the World [Unabridged]" and "The Eye of the
// World" produce different keys.
var bracketSuffixRe = regexp.MustCompile(`\s*\[[^\[\]]*\]\s*$`)

// StripBracketSuffixes removes any trailing square-bracketed qualifiers,
// applied repeatedly so "Title [Unabridged] [2021]" is fully cleaned.
func StripBracketSuffixes(title string) string {
	for {
		stripped := bracketSuffixRe.ReplaceAllString(title, "")
		if stripped == title {
			return strings.TrimSpace(stripped)
		}
		title = stripped
	}
}

// CanonicalDedupKey is the single, authoritative dedup key used to decide
// whether two book rows describe the same work across importers (Calibre,
// Audiobookshelf, CWA, manual). It MUST be the only function any book-creation
// path uses to populate books.dedup_key and the only function any lookup uses
// to key by author+title, so the key is computed identically on every side and
// the previous asymmetry (#940 — Calibre matched on raw LOWER(title) SQL while
// ABS matched on a normalized in-memory key) cannot recur.
//
// It strips trailing bracketed qualifiers first (ABS-style "[Unabridged]"),
// then applies NormalizeTitleForDedup (paren-suffix strip, punctuation fold,
// whitespace/Unicode/umlaut folding, lowercasing). The result is
// case-insensitive and stable, so it is stored verbatim and compared with =.
//
// Key equality means SAME WORK, with no further evidence required. It does NOT
// mean the converse: two rows for one work can hold different keys when one
// side carries a subtitle the other omits. That case is a *candidate*, not a
// match, and belongs to CompareTitles / MainTitleKey — see #2042.
func CanonicalDedupKey(title string) string {
	return NormalizeTitleForDedup(StripBracketSuffixes(strings.TrimSpace(title)))
}

// MainTitleKey is the canonical key of the title with any ": subtitle" tail
// removed. It is the BLOCKING key: two rows for the same work always share it,
// including when one side spells out a subtitle the other drops. It is
// deliberately weaker than CanonicalDedupKey — "Star Wars: A New Hope" and
// "Star Wars: The Empire Strikes Back" share a main-title key and are still
// different books — so it is only ever used to gather candidates that
// CompareTitles then adjudicates. Never store it as an identity.
//
// For a title with no subtitle, MainTitleKey == CanonicalDedupKey.
func MainTitleKey(title string) string {
	main, _ := SplitSubtitle(title)
	return NormalizeTitleForDedup(main)
}

// SplitSubtitle splits a title into its main part and its subtitle at the
// first ": " (colon followed by whitespace), after trailing bracketed and
// parenthesised qualifiers have been removed so that
// "Mistborn: The Final Empire [Unabridged]" yields ("Mistborn", "The Final
// Empire") rather than leaking the tag into the subtitle. A compact "foo:bar"
// with no whitespace after the colon is not a subtitle and is left whole.
// The returned subtitle is "" when the title carries none.
func SplitSubtitle(title string) (main, subtitle string) {
	cleaned := newznab.NormalizeQueryTitle(norm.NFC.String(StripBracketSuffixes(strings.TrimSpace(title))))
	for i := 0; i < len(cleaned)-1; i++ {
		if cleaned[i] == ':' && (cleaned[i+1] == ' ' || cleaned[i+1] == '\t') {
			return strings.TrimSpace(cleaned[:i]), strings.TrimSpace(cleaned[i+1:])
		}
	}
	return cleaned, ""
}

// TitleVerdict is the outcome of comparing two titles for the same author. It
// is deliberately three-valued: the two-valued question "are these the same
// book?" cannot be answered from a title pair alone when exactly one side
// carries a subtitle, and forcing an answer is what produced both bugs in
// #2042.
type TitleVerdict int

const (
	// TitlesDifferent means the pair is positively known to be two works.
	// Callers must not merge them.
	TitlesDifferent TitleVerdict = iota
	// TitlesSame means the canonical keys are equal: one work, no further
	// evidence needed.
	TitlesSame
	// TitlesNeedCorroboration means the pair shares a main title but exactly
	// one side carries a subtitle ("The Eye of the World" vs "The Eye of the
	// World: Book One of The Wheel of Time"). That is usually one work with a
	// publisher subtitle the other source dropped, and occasionally two. A
	// caller that has a corroborating signal — series name plus sequence,
	// ISBN, ASIN — MUST use it. A caller with no such signal must make its
	// choice explicitly and document it; it must never silently treat this as
	// TitlesSame without saying so.
	TitlesNeedCorroboration
)

func (v TitleVerdict) String() string {
	switch v {
	case TitlesSame:
		return "same"
	case TitlesNeedCorroboration:
		return "needs-corroboration"
	default:
		return "different"
	}
}

// CompareTitles decides whether two titles (already known to share an author)
// describe the same work. It is symmetric: CompareTitles(a, b) always equals
// CompareTitles(b, a).
//
// The rules, in order:
//
//	canonical keys equal                    → TitlesSame
//	main-title keys differ                  → TitlesDifferent
//	both sides have subtitles, and they
//	  differ (implied by the first rule)     → TitlesDifferent
//	exactly one side has a subtitle          → TitlesNeedCorroboration
//
// A blank key on either side is never a match: an untitled row must not
// collapse onto every other untitled row.
func CompareTitles(a, b string) TitleVerdict {
	ka, kb := CanonicalDedupKey(a), CanonicalDedupKey(b)
	if ka == "" || kb == "" {
		return TitlesDifferent
	}
	if ka == kb {
		return TitlesSame
	}
	mainA, subA := SplitSubtitle(a)
	mainB, subB := SplitSubtitle(b)
	if NormalizeTitleForDedup(mainA) != NormalizeTitleForDedup(mainB) {
		return TitlesDifferent
	}
	// Main titles agree but the full keys do not, so at least one subtitle is
	// present. If both are, they must differ — two named instalments of one
	// series ("Star Wars: A New Hope" / ": The Empire Strikes Back"). That is
	// the false-positive the old truncating key merged.
	if subA != "" && subB != "" {
		return TitlesDifferent
	}
	return TitlesNeedCorroboration
}

// TitleIndex is the in-memory equivalent of the two-tier lookup the book repo
// performs in SQL: a set of titles that can be probed for "do I already have
// this work?" without a database round trip.
//
// A plain map[CanonicalDedupKey]T is not sufficient any more. The canonical key
// is lossless, so a stored "Mistborn: The Final Empire" and an incoming
// "Mistborn" hash to different buckets even though they are one work. The index
// therefore keeps a second bucket keyed by MainTitleKey and adjudicates
// collisions with CompareTitles, exactly as dedupCandidates does.
//
// Lookup prefers an exact (TitlesSame) hit and only falls back to a
// TitlesNeedCorroboration one, so a subtitled near-miss can never shadow the
// row that actually matches. As in SQL, callers reaching the fallback tier are
// accepting a one-sided subtitle as the same work; that is the pre-#2042
// behaviour and is right far more often than not.
type TitleIndex[T any] struct {
	exact  map[string]T
	byMain map[string][]titleEntry[T]
}

type titleEntry[T any] struct {
	title string
	value T
}

// NewTitleIndex returns an empty index.
func NewTitleIndex[T any]() *TitleIndex[T] {
	return &TitleIndex[T]{
		exact:  make(map[string]T),
		byMain: make(map[string][]titleEntry[T]),
	}
}

// Add records title → value. A repeated canonical key overwrites, matching the
// last-writer-wins semantics of the plain map this type replaces. Titles with
// an empty canonical key are ignored: an untitled row must not become a match
// for every other untitled row.
func (ix *TitleIndex[T]) Add(title string, value T) {
	key := CanonicalDedupKey(title)
	if key == "" {
		return
	}
	ix.exact[key] = value
	if main := MainTitleKey(title); main != "" && main != key {
		ix.byMain[main] = append(ix.byMain[main], titleEntry[T]{title: title, value: value})
	}
}

// Lookup returns the value recorded for a title describing the same work, and
// whether one was found.
func (ix *TitleIndex[T]) Lookup(title string) (T, bool) {
	var zero T
	key := CanonicalDedupKey(title)
	if key == "" {
		return zero, false
	}
	if v, ok := ix.exact[key]; ok {
		return v, true
	}
	main := MainTitleKey(title)
	if main == "" {
		return zero, false
	}
	// The incoming title carries a subtitle the stored row dropped: its main
	// key is that row's canonical key.
	if main != key {
		if v, ok := ix.exact[main]; ok {
			return v, true
		}
	}
	// The stored row carries a subtitle this title omits.
	for _, e := range ix.byMain[main] {
		if CompareTitles(title, e.title) != TitlesDifferent {
			return e.value, true
		}
	}
	return zero, false
}
