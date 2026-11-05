package indexer

import (
	"regexp"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// Narration is what a release title says about the recording (#1358). Both
// fields are "unknown" (nil, "") unless the title says it unambiguously.
// Ambiguity yields unknown, never a guess: ranking and rejection act on what
// is known, and unknown is never held against a release.
type Narration struct {
	Narrators   []string
	EditionType string
}

var (
	// Edition markers. \b keeps "unabridged" from also matching "abridged",
	// because there is no word boundary between the "n" and the "a".
	narrUnabridgedRe = regexp.MustCompile(`(?i)\bunabridged\b`)
	narrAbridgedRe   = regexp.MustCompile(`(?i)\babridged\b`)
	narrDramatizedRe = regexp.MustCompile(`(?i)\bdramati[sz](?:ed|ation)\b`)
	narrFullCastRe   = regexp.MustCompile(`(?i)\bfull[\s\-]+cast\b`)
	// "not abridged", "non-abridged": a negation, so not evidence of the marker.
	narrNegatedRe = regexp.MustCompile(`(?i)\b(?:not|non)[\s\-]+(?:un)?abridged\b`)

	// Delimited groups: (...) and [...].
	narrGroupRe = regexp.MustCompile(`[\(\[][^\)\]]*[\)\]]`)

	// "read by X", "narrated by X", "performed by X". The name runs to a
	// bracket, a spaced dash or pipe, a comma-separated tail that starts a new
	// field, or the end of the title.
	narrByRe   = regexp.MustCompile(`(?i)\b(?:read|narrated|performed)\s+by\s+([^\[\(\)\]\|]+)`)
	narrDashRe = regexp.MustCompile(`\s[-\x{2013}]\s`)
	// "X (narrator)" at the start of the title or after a separator.
	narrTagRe = regexp.MustCompile(`(?i)(?:^|\s[-\x{2013}]\s|\|\s*)([^\-\x{2013}\|\[\(]+?)\s*[\(\[]\s*narrator\s*[\)\]]`)

	// Credits after the narrator: "translated by X", "edited by X", and so on.
	narrOtherRoleRe = regexp.MustCompile(`(?i)[,;]?\s*\b(?:translated|translator|edited|illustrated|introduced|written|abridged)\s+by\b.*$`)
	// A dash that opens a new field, with or without a closing space:
	// " - Dune", " -Unabridged-".
	narrDashCutRe = regexp.MustCompile(`\s[-\x{2013}\x{2014}].*$`)
)

// ParseNarration reads the edition type and narrators a release title states.
// bookTitles and authors are the wanted book's own title and author names; they
// are removed before the title is read so a book called "The Abridged History
// of Rome", or an author named like a narrator, is not mistaken for a marker.
func ParseNarration(title string, bookTitles []string, authors []string) Narration {
	n := Narration{Narrators: []string{}}
	n.EditionType = parseEditionType(title, bookTitles, authors)
	n.Narrators = parseNarrators(title, bookTitles, authors)
	return n
}

// stripKnown removes the first case-insensitive occurrence of each known
// phrase from s and reports whether any title was found.
func stripKnown(s string, phrases []string) (string, bool) {
	found := false
	for _, p := range phrases {
		p = strings.TrimSpace(p)
		if len(p) < 3 {
			continue
		}
		if i := strings.Index(strings.ToLower(s), strings.ToLower(p)); i >= 0 {
			s = s[:i] + " " + s[i+len(p):]
			found = true
		}
	}
	return s, found
}

func parseEditionType(title string, bookTitles, authors []string) string {
	// Bracketed groups count on their own: "(Unabridged)", "[Abridged]".
	// Bare words count only after the wanted book's title has been located and
	// removed, so the word cannot be part of the book's name. With no known
	// title in the release a bare word is not read at all.
	groups := strings.Join(narrGroupRe.FindAllString(title, -1), " ")
	rest := narrGroupRe.ReplaceAllString(title, " ")
	rest, titleFound := stripKnown(rest, bookTitles)
	rest, _ = stripKnown(rest, authors)

	// Bracketed text is checked with the title removed too, for a book whose
	// own name is bracketed in the release.
	groups, _ = stripKnown(groups, bookTitles)

	var text string
	if titleFound {
		text = groups + " " + rest
	} else {
		text = groups
	}
	text = narrNegatedRe.ReplaceAllString(text, " ")

	unabridged := narrUnabridgedRe.MatchString(text)
	abridged := narrAbridgedRe.MatchString(text)
	if unabridged && abridged {
		return ""
	}
	dramatized := narrDramatizedRe.MatchString(text)
	fullCast := narrFullCastRe.MatchString(text)
	switch {
	case dramatized && fullCast:
		return ""
	case dramatized:
		return models.EditionTypeDramatized
	case fullCast:
		if abridged {
			return ""
		}
		return models.EditionTypeFullCast
	case unabridged:
		return models.EditionTypeUnabridged
	case abridged:
		return models.EditionTypeAbridged
	}
	return ""
}

// parseNarrators accepts only the explicit shapes "read by", "narrated by",
// "performed by" and "NAME (narrator)". A bare name anywhere else in a title is
// never a narrator, and "by NAME" alone is the author credit. Two shapes that
// disagree give unknown.
func parseNarrators(title string, bookTitles, authors []string) []string {
	// The wanted book's own title is removed first, so "Why I Read By
	// Candlelight" is a title and not a "read by" credit.
	title, _ = stripKnown(title, bookTitles)
	var found [][]string
	for _, m := range narrByRe.FindAllStringSubmatch(title, -1) {
		credit := narrDashRe.Split(m[1], 2)[0]
		if names := splitReleaseNames(credit); len(names) > 0 {
			found = append(found, names)
		}
	}
	for _, m := range narrTagRe.FindAllStringSubmatch(title, -1) {
		if names := tagNames(m[1], authors); len(names) > 0 {
			found = append(found, names)
		}
	}
	if len(found) == 0 {
		return []string{}
	}
	first := found[0]
	for _, other := range found[1:] {
		if !sameNames(first, other) {
			return []string{}
		}
	}
	return first
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if models.FoldName(a[i]) != models.FoldName(b[i]) {
			return false
		}
	}
	return true
}

// narrStopWords are words that follow a narrator's name in a release title
// (format, language, publisher, edition words) and are never part of one.
var narrStopWords = map[string]bool{
	"audible": true, "retail": true, "english": true, "epub": true, "pdf": true,
	"mobi": true, "azw": true, "azw3": true, "m4b": true, "m4a": true, "mp3": true,
	"flac": true, "ogg": true, "aac": true, "audiobook": true, "audiobooks": true,
	"unabridged": true, "abridged": true, "dramatized": true, "dramatised": true,
	"studios": true, "audio": true, "publishing": true, "press": true, "records": true,
	"productions": true, "entertainment": true, "llc": true, "inc": true, "ltd": true,
	"books": true, "kbps": true,
}

func isStopToken(w string) bool {
	w = strings.ToLower(strings.Trim(w, ".-_()[]"))
	if narrStopWords[w] {
		return true
	}
	if len(w) == 4 && (strings.HasPrefix(w, "19") || strings.HasPrefix(w, "20")) {
		return true
	}
	return strings.HasSuffix(w, "kbps")
}

// splitReleaseNames turns the text after "read by" into names. The text is cut
// at another credit ("translated by"), at a dash field and at trailing format,
// publisher or edition words. A name list stops at the first entry that is
// nothing but such words ("Scott Brick, Audible Studios"). Every remaining name
// must look like a person; one that does not makes the credit unknown, and so
// does a credit that starts "the author", which names a role and not a person.
func splitReleaseNames(s string) []string {
	s = narrOtherRoleRe.ReplaceAllString(s, "")
	s = narrDashCutRe.ReplaceAllString(" "+s, "")
	s = strings.NewReplacer(" & ", ",", " and ", ",", " And ", ",", " with ", ",", " With ", ",", ";", ",").Replace(s)
	var out []string
	for _, part := range strings.Split(s, ",") {
		words := strings.Fields(part)
		for len(words) > 0 && isStopToken(words[len(words)-1]) {
			words = words[:len(words)-1]
		}
		if len(words) == 0 {
			break
		}
		if strings.EqualFold(words[0], "the") {
			return nil
		}
		name := strings.Join(words, " ")
		if !plausibleName(name) {
			return nil
		}
		out = append(out, name)
	}
	return models.CleanNarrators(out)
}

// tagNames reads the text before "(narrator)". With several fields separated by
// a semicolon only the last one is the narrator. A list of names loses any that
// are the book's author, because an author listed beside a narrator is the
// author credit. A lone name is kept, as it is stated outright.
func tagNames(s string, authors []string) []string {
	if i := strings.LastIndex(s, ";"); i >= 0 {
		s = s[i+1:]
	}
	names := splitReleaseNames(s)
	if len(names) < 2 || len(authors) == 0 {
		return names
	}
	isAuthor := map[string]bool{}
	for _, a := range authors {
		if k := models.FoldName(a); k != "" {
			isAuthor[k] = true
		}
	}
	kept := names[:0:0]
	for _, n := range names {
		if !isAuthor[models.FoldName(n)] {
			kept = append(kept, n)
		}
	}
	return kept
}

func plausibleName(s string) bool {
	words := strings.Fields(s)
	if len(words) < 2 || len(words) > 5 || len(s) > 60 {
		return false
	}
	for _, r := range s {
		switch {
		case r == ' ' || r == '.' || r == '\'' || r == '-' || r == '’':
		case r >= '0' && r <= '9':
			return false
		case r < 128 && (r < 'A' || r > 'Z' && r < 'a' || r > 'z'):
			return false
		}
	}
	return true
}

// narratorRank returns the index of the first of preferred that matches any
// of narrators by whole folded name, or -1.
func narratorRank(narrators, preferred []string) int {
	if len(narrators) == 0 || len(preferred) == 0 {
		return -1
	}
	want := make(map[string]bool, len(narrators))
	for _, n := range narrators {
		want[models.FoldName(n)] = true
	}
	for i, p := range preferred {
		if k := models.FoldName(p); k != "" && want[k] {
			return i
		}
	}
	return -1
}

// ReleaseNarration parses a result title against the wanted book.
func (c MatchCriteria) ReleaseNarration(title string) Narration {
	authors := append([]string{c.Author}, c.AuthorAliases...)
	return ParseNarration(title, []string{c.Title}, authors)
}

// EditionTypeScore is the extra ranking adjustment a profile's edition
// preference adds for a parsed edition type. Off (the default) adds nothing.
func EditionTypeScore(preference, editionType string) float64 {
	switch preference {
	case models.EditionPrefPreferUnabridged, models.EditionPrefRequireUnabridged:
		switch editionType {
		case models.EditionTypeUnabridged:
			return 100
		case models.EditionTypeAbridged:
			return -200
		}
	}
	return 0
}

// NarratorScore is the bonus for a release narrated by a preferred narrator,
// larger for earlier entries. No match, or no preference, adds nothing.
func NarratorScore(preferred, narrators []string) float64 {
	i := narratorRank(narrators, preferred)
	if i < 0 {
		return 0
	}
	bonus := 150 - 20*i
	if bonus < 30 {
		bonus = 30
	}
	return float64(bonus)
}
