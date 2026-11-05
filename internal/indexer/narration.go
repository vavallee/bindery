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

	narrTrailRe = regexp.MustCompile(`(?i)\s+(?:unabridged|abridged|dramati[sz]ed|full[\s\-]+cast|audiobook|m4b|mp3|flac|m4a|ogg|\d{4})\b.*$`)
)

// ParseNarration reads the edition type and narrators a release title states.
// bookTitles and authors are the wanted book's own title and author names; they
// are removed before the title is read so a book called "The Abridged History
// of Rome", or an author named like a narrator, is not mistaken for a marker.
func ParseNarration(title string, bookTitles []string, authors []string) Narration {
	n := Narration{Narrators: []string{}}
	n.EditionType = parseEditionType(title, bookTitles, authors)
	n.Narrators = parseNarrators(title)
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
func parseNarrators(title string) []string {
	var found [][]string
	for _, m := range narrByRe.FindAllStringSubmatch(title, -1) {
		credit := narrDashRe.Split(m[1], 2)[0]
		if names := splitReleaseNames(credit); len(names) > 0 {
			found = append(found, names)
		}
	}
	for _, m := range narrTagRe.FindAllStringSubmatch(title, -1) {
		if names := splitReleaseNames(m[1]); len(names) > 0 {
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

// splitReleaseNames turns the text after "read by" into names. Every name must
// look like a person: two to five words, letters and name punctuation only. One
// bad name makes the whole credit unknown.
func splitReleaseNames(s string) []string {
	s = narrTrailRe.ReplaceAllString(" "+s, "")
	s = strings.NewReplacer(" & ", ",", " and ", ",", " And ", ",", ";", ",").Replace(s)
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.Join(strings.Fields(part), " ")
		if part == "" {
			continue
		}
		if !plausibleName(part) {
			return nil
		}
		out = append(out, part)
	}
	return models.CleanNarrators(out)
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
