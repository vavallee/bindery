package indexer

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// NormalizeTitleForDedup returns a canonical form of title used as the
// deduplication key when comparing book rows. The normalization is applied
// symmetrically: both when seeding the "already-seen" set from existing DB
// rows and when keying incoming provider results. This guarantees that two
// rows for the same work only differ in edition qualifier, whitespace,
// Unicode form, or umlaut representation are collapsed to the same key.
//
// Steps applied (in order):
//  1. Unicode NFC — composes combining characters into precomposed forms,
//     so "é" (NFD) and "é" (NFC) produce the same key.
//  2. newznab.NormalizeQueryTitle — folds smart quotes to ASCII, strips a
//     trailing parenthesised edition qualifier ("(German Edition)" etc.),
//     and collapses internal whitespace.
//  3. stripSubtitle — drops a ": subtitle" tail so editions that differ
//     only in whether the subtitle is present (audiobooks often drop it)
//     produce the same key.
//  4. strings.ToLower — case-insensitive match.
//  5. transliterateUmlauts — maps ä→ae, ö→oe, ü→ue, ß→ss so that
//     "Geraeusch" from a release title compares equal to "Geräusch" from
//     the metadata provider.
func NormalizeTitleForDedup(title string) string {
	title = norm.NFC.String(title)
	title = newznab.NormalizeQueryTitle(title)
	title = stripSubtitle(title)
	title = strings.ToLower(title)
	title = transliterateUmlauts(title)
	return title
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
// then applies NormalizeTitleForDedup (paren-suffix strip, subtitle strip,
// whitespace/Unicode/umlaut folding, lowercasing). The result is
// case-insensitive and stable, so it is stored verbatim and compared with =.
func CanonicalDedupKey(title string) string {
	return NormalizeTitleForDedup(StripBracketSuffixes(strings.TrimSpace(title)))
}

// stripSubtitle removes a trailing ": subtitle" segment when the colon is
// followed by whitespace. Collapses editions that vary only in whether the
// subtitle is present (e.g. audiobook drops it, ebook keeps it). Compact
// titles like "foo:bar" with no whitespace after the colon are left intact.
func stripSubtitle(title string) string {
	for i := 0; i < len(title)-1; i++ {
		if title[i] == ':' && (title[i+1] == ' ' || title[i+1] == '\t') {
			return strings.TrimSpace(title[:i])
		}
	}
	return title
}
