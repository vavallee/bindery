package indexer

import (
	"strings"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// titleIdentityWords keeps numbers as well as significant words: dropping "12"
// lets "12 More Rules for Life" satisfy "12 Rules for Life".
func titleIdentityWords(s string) []string {
	var words []string
	for _, word := range strings.Fields(NormalizeRelease(s)) {
		if isAllDigits(word) || len(newznab.SigWords(word)) != 0 {
			words = append(words, word)
		}
	}
	return words
}

// conflictingTitleAuthor recognises explicit trailing attribution only when
// the left side names the requested title. Title-only releases remain valid;
// release metadata and narrator credits are not treated as authors.
//
// Both sides are compared with volume markers folded to one spelling
// (foldVolumeMarkers), because the keyword matchers accept "Vol 17" for a
// "Volume 17" title: without the fold, "Title Vol 17 - Other Author" would be
// accepted on its title while its attribution went unread.
func conflictingTitleAuthor(release, title string, authorSets [][]string) bool {
	normalizedTitle := foldVolumeMarkers(NormalizeRelease(title))
	normalizedRelease := foldVolumeMarkers(NormalizeRelease(release))
	var attribution string
	if after, ok := strings.CutPrefix(normalizedRelease, normalizedTitle+" by "); ok {
		attribution = after
	} else {
		for _, separator := range []string{" - ", " – ", " — "} {
			before, after, ok := strings.Cut(release, separator)
			if ok && foldVolumeMarkers(NormalizeRelease(before)) == normalizedTitle {
				attribution = NormalizeRelease(after)
				break
			}
		}
	}
	if attribution == "" {
		attribution = seriesLabelAttribution(normalizedRelease, normalizedTitle)
	}
	if attribution == "" {
		return false
	}
	words := newznab.SigWords(attribution)
	if len(words) == 0 || benignTrailingToken(words[0], nil) {
		return false
	}
	knownAuthor := false
	for _, tokens := range authorSets {
		if len(tokens) == 0 {
			continue
		}
		knownAuthor = true
		if authorMatchesRelease(attribution, tokens) {
			return false
		}
		// A surname-only credit is not evidence of a conflicting author.
		if words[0] == tokens[len(tokens)-1] && (len(words) == 1 || benignTrailingToken(words[1], nil)) {
			return false
		}
	}
	return knownAuthor
}

// foldVolumeMarkers rewrites every volume marker spelling in a
// NormalizeRelease string to "vol", so two strings that differ only in how
// they spell the marker compare equal. Used for equality tests only.
func foldVolumeMarkers(s string) string {
	toks := strings.Fields(s)
	changed := false
	for i, tok := range toks {
		if newznab.IsVolumeMarker(tok) && tok != "vol" {
			toks[i] = "vol"
			changed = true
		}
	}
	if !changed {
		return s
	}
	return strings.Join(toks, " ")
}

// maxSeriesLabelTokens caps how many words may sit between the requested title
// and a later "by" for that "by" to still be read as the book's attribution.
// A series name plus an index ("Skye Druids 03", "Home Repair Is Homicide 12")
// fits comfortably; a longer run is more likely a different, longer title or
// free text, which the title keyword checks already judge.
const maxSeriesLabelTokens = 8

// creditRoleWords are the words that turn a following "by" into a credit that
// is not the author: a narrator, translator, illustrator, or the uploader of
// the release. "Narrated by Donna Grant" says nothing about who wrote the book.
var creditRoleWords = map[string]bool{
	"read": true, "narrated": true, "narrator": true, "performed": true,
	"translated": true, "translation": true, "illustrated": true, "illustrations": true,
	"foreword": true, "introduction": true, "afterword": true, "introduced": true,
	"uploaded": true, "posted": true, "ripped": true, "encoded": true,
	"scanned": true, "shared": true, "you": true, // "brought to you by"
}

// seriesLabelAttribution reads "<title>, <series> <index> by <author>" (#2863),
// where a series label sits between the requested title and the credit so the
// "<title> by <author>" prefix check in conflictingTitleAuthor never sees it:
// "Heart of Glass, Skye Druids (03) by Donna Grant EPUB".
//
// Both arguments are NormalizeRelease output, so punctuation and brackets are
// already gone and "by" can only match as a whole word, never inside one
// ("Abby"). Only the part of the release AFTER the requested title is searched,
// so a "by" inside the title itself ("Stand by Me", "Death by Chocolate") is
// consumed by the title prefix and not mistaken for an attribution.
//
// The attribution is the words after the first such "by", up to the first
// token that describes the file rather than a person (a format, a release
// marker such as "retail", or a year or other digit led token). It returns ""
// when there is no such "by", when the label before it is implausibly long, or
// when the "by" credits a narrator, translator or uploader; the caller then
// accepts the release on its title exactly as before.
func seriesLabelAttribution(normalizedRelease, normalizedTitle string) string {
	if normalizedTitle == "" {
		return ""
	}
	rest, ok := strings.CutPrefix(normalizedRelease, normalizedTitle+" ")
	if !ok {
		return ""
	}
	toks := strings.Fields(rest)
	for i, tok := range toks {
		if tok != "by" {
			continue
		}
		if i == 0 || i > maxSeriesLabelTokens || creditRoleWords[toks[i-1]] {
			return ""
		}
		var attribution []string
		for _, a := range toks[i+1:] {
			if attributionStopToken(a) {
				break
			}
			attribution = append(attribution, a)
		}
		return strings.Join(attribution, " ")
	}
	return ""
}

// attributionStopToken reports whether tok ends a "by <author>" attribution:
// it describes the file (format, release marker) or is digit led (a year,
// bitrate or size), none of which is part of a person's name.
func attributionStopToken(tok string) bool {
	if tok[0] >= '0' && tok[0] <= '9' {
		return true
	}
	if releaseMetaTokens[tok] {
		return true
	}
	for _, f := range formatTokens {
		if tok == f {
			return true
		}
	}
	return false
}
