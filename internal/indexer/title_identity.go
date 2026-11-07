package indexer

import (
	"slices"
	"strings"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/textutil"
)

// titleIdentityWords keeps numbers as well as significant words: dropping "12"
// lets "12 More Rules for Life" satisfy "12 Rules for Life".
//
// This is the RELEASE side: words in the release alphabet, umlauts expanded,
// meant to be joined into a haystack. The requested title's own words come
// from titleIdentityKeywords, which keeps the umlaut so the phrase match
// against this haystack knows which digraphs may be compact (#3123).
func titleIdentityWords(s string) []string {
	return identityWords(NormalizeRelease(s))
}

// titleIdentityKeywords is titleIdentityWords for the requested title: the same
// words, but umlauts kept (textutil.FoldForKeyword) so ContainsPhrase can make
// exactly those digraphs optional. Not for use as a haystack.
func titleIdentityKeywords(s string) []string {
	return identityWords(textutil.FoldForKeyword(s))
}

func identityWords(folded string) []string {
	var words []string
	for _, word := range strings.Fields(folded) {
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
// Both sides are compared with volume markers folded to one spelling and
// numbers stripped of zero padding (foldTitleTokens), because the keyword
// matchers accept "Vol 07" for a "Volume 7" title: without the fold,
// "Title Vol 07 - Other Author" would be accepted on its title while its
// attribution went unread.
func conflictingTitleAuthor(release, title string, authorSets [][]string) bool {
	normalizedTitle := foldTitleTokens(NormalizeRelease(title))
	normalizedRelease := foldTitleTokens(NormalizeRelease(release))
	var attribution string
	if after, ok := strings.CutPrefix(normalizedRelease, normalizedTitle+" by "); ok {
		attribution = after
	} else {
		for _, separator := range []string{" - ", " – ", " — "} {
			before, after, ok := strings.Cut(release, separator)
			if ok && foldTitleTokens(NormalizeRelease(before)) == normalizedTitle {
				attribution = NormalizeRelease(after)
				break
			}
		}
	}
	if attribution == "" {
		// The two readings below find a "by" further from the title, where it
		// is more often a narrator's credit with no role word. A release that
		// names the wanted author before that "by" ("Pride & Prejudice (Jane
		// Austen) by Rosamund Pike") is not read as crediting someone else.
		// After the credit the author's name proves nothing: "by Donna Grant
		// [for fans of Jennifer Hillier]" is still Donna Grant's book (#2863).
		attribution = seriesLabelAttribution(normalizedRelease, normalizedTitle)
		if attribution == "" {
			attribution = locatedTitleAttribution(normalizedRelease, title)
		}
		if attribution != "" {
			if at := strings.Index(normalizedRelease, "by "+attribution); at >= 0 &&
				namesWantedAuthor(normalizedRelease[:at], authorSets) {
				return false
			}
		}
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
		// words come from the release, in its alphabet; the token is a
		// keyword that may hold a kept umlaut, so expand it before comparing.
		if words[0] == textutil.TransliterateUmlauts(tokens[len(tokens)-1]) && (len(words) == 1 || benignTrailingToken(words[1], nil)) {
			return false
		}
	}
	return knownAuthor
}

// namesWantedAuthor reports whether the release names one of the author token
// sets anywhere.
func namesWantedAuthor(normalizedRelease string, authorSets [][]string) bool {
	for _, tokens := range authorSets {
		if len(tokens) > 0 && authorMatchesRelease(normalizedRelease, tokens) {
			return true
		}
	}
	return false
}

// foldTitleTokens rewrites every volume marker spelling in a
// NormalizeRelease string to "vol" and strips zero padding from every number
// ("07" to "7", "00" to "0"), so two strings that differ only in how they
// spell the marker or pad a number compare equal. This mirrors what
// keywordPattern accepts. Used for equality tests only.
func foldTitleTokens(s string) string {
	toks := strings.Fields(s)
	changed := false
	for i, tok := range toks {
		if newznab.IsVolumeMarker(tok) && tok != "vol" {
			toks[i] = "vol"
			changed = true
		} else if len(tok) > 1 && tok[0] == '0' && isAllDigits(tok) {
			if toks[i] = strings.TrimLeft(tok, "0"); toks[i] == "" {
				toks[i] = "0"
			}
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
	"edited": true, "compiled": true, "adapted": true, "annotated": true,
	"published": true, "produced": true, "presented": true, "told": true,
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
	return byCredit(strings.Fields(rest), false)
}

// byCredit reads the first "by" credit in toks, the release words that follow
// the requested title. The attribution is the words after that "by", up to the
// first attributionStopToken. It returns "" when there is no "by", when more
// than maxSeriesLabelTokens words sit before it, when the "by" credits a
// narrator, translator or uploader (creditRoleWords), or when the "by" opens
// toks and leading is false.
func byCredit(toks []string, leading bool) string {
	for i, tok := range toks {
		if tok != "by" {
			continue
		}
		if (i == 0 && !leading) || i > maxSeriesLabelTokens || (i > 0 && creditRoleWords[toks[i-1]]) {
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

// locatedTitleAttribution reads a "by" credit after the requested title when
// the release does not START with that title (#3615). The two readings above
// need the release to open with the title, word for word, so a credit was
// never read from "The Last Ride by Nick Louth" for "Last Ride" (a leading
// article), "Smoke & Mirrors by E A Copen" for "Smoke and Mirrors" (the
// ampersand), or "The Mirror in the Mirror: A Labyrinth by Michael Ende" for
// "Mirror Mirror" (stop words between the title words). Each was then accepted
// on its title words alone, although it names another author outright.
//
// The title is found with the same phrase match the keyword and identity
// checks use, so this reads a credit exactly where those checks found the
// title. Insignificant words that end the title ("Stand by Me") are consumed
// with it, so a "by" inside the title is not read as the credit.
//
// A release that names the requested author before the "by" ("Jim Butcher -
// Mirror Mirror by James Marsters") is not a conflict: the caller checks that
// (namesWantedAuthor), since the "by" there is most likely a narrator credit
// without its role word.
func locatedTitleAttribution(normalizedRelease, title string) string {
	ident := titleIdentityKeywords(title)
	if len(ident) == 0 {
		return ""
	}
	loc := phraseRegex(ident).FindStringIndex(normalizedRelease)
	if loc == nil {
		return ""
	}
	rest := strings.Fields(normalizedRelease[loc[1]:])
	titleToks := strings.Fields(foldTitleTokens(NormalizeRelease(title)))
	last := -1
	for i, tok := range titleToks {
		if isAllDigits(tok) || len(newznab.SigWords(tok)) != 0 {
			last = i
		}
	}
	if tail := titleToks[last+1:]; len(tail) > 0 && len(rest) >= len(tail) && slices.Equal(rest[:len(tail)], tail) {
		rest = rest[len(tail):]
	}
	return byCredit(rest, true)
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
