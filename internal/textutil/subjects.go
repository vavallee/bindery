package textutil

import (
	"regexp"
	"strings"
	"unicode"
)

// junkSubjectPrefixes are machine tags OpenLibrary mixes into a work's
// subjects ("nyt:hardcover-fiction=2015-01-25", "place:Mars",
// "person:Darrow", "time:Future"). They are catalogue bookkeeping, not
// genres, and read as noise when shown as tags.
var junkSubjectPrefixes = []string{"nyt:", "place:", "person:", "time:"}

// gluedSubjectPrefixes are words OpenLibrary glues onto a value with no space
// ("seriesRed Rising Trilogy", "series:Red_Rising", "collectionID:..."). They
// count as junk only when glued, so a real subject that merely starts with
// the word ("Series fiction", "Collections") is kept.
var gluedSubjectPrefixes = []string{"series", "collection"}

// IsJunkSubject reports whether a subject string is a machine tag rather than
// a genre: one of junkSubjectPrefixes, one of gluedSubjectPrefixes followed
// directly by a colon, underscore, capital letter or digit, or anything with
// an "=" in it. Matching ignores case.
func IsJunkSubject(subject string) bool {
	s := strings.TrimSpace(subject)
	if strings.Contains(s, "=") {
		return true
	}
	lower := strings.ToLower(s)
	for _, p := range junkSubjectPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	next, ok := gluedPrefixNext(s)
	return ok && (next == ':' || gluedValueStart(next))
}

// gluedPrefixNext returns the character right after a gluedSubjectPrefixes
// word that s starts with, ignoring case.
func gluedPrefixNext(s string) (rune, bool) {
	lower := strings.ToLower(s)
	for _, p := range gluedSubjectPrefixes {
		if len(s) > len(p) && strings.HasPrefix(lower, p) {
			return []rune(s[len(p):])[0], true
		}
	}
	return 0, false
}

// gluedValueStart reports whether r starts a value glued straight onto the
// prefix word: an underscore, a capital letter or a digit.
func gluedValueStart(r rune) bool {
	return r == '_' || unicode.IsUpper(r) || unicode.IsDigit(r)
}

// olMachineTagRE is the exact shape OpenLibrary writes its machine tags in: a
// known key, a colon and the value with no space between
// ("nyt:hardcover-fiction=2015-01-25", "place:mars", "collectionID:x").
var olMachineTagRE = regexp.MustCompile(`(?i)^(?:nyt|place|person|time|series|collection|collectionid|award|subject):\S`)

// IsOpenLibraryMachineTag is the narrow form of IsJunkSubject, for genres that
// may have come from somewhere other than OpenLibrary (an Audiobookshelf
// import, a hand edit). It matches only the shapes OpenLibrary itself writes:
// "key:value" with no space after the colon, or "series"/"collection" glued to
// a value. "Time: Future" or "E=mc2" typed by a person is not matched.
func IsOpenLibraryMachineTag(subject string) bool {
	s := strings.TrimSpace(subject)
	if olMachineTagRE.MatchString(s) {
		return true
	}
	next, ok := gluedPrefixNext(s)
	return ok && gluedValueStart(next)
}

// DropJunkSubjects returns subjects without the entries IsJunkSubject
// rejects, preserving order. It returns the input slice unchanged when there
// is nothing to drop, and never returns nil for a non-nil input.
func DropJunkSubjects(subjects []string) []string {
	return dropSubjects(subjects, IsJunkSubject)
}

// DropOpenLibraryMachineTags is DropJunkSubjects with the narrow
// IsOpenLibraryMachineTag rule.
func DropOpenLibraryMachineTags(subjects []string) []string {
	return dropSubjects(subjects, IsOpenLibraryMachineTag)
}

func dropSubjects(subjects []string, junk func(string) bool) []string {
	keep := -1
	for i, s := range subjects {
		if junk(s) {
			keep = i
			break
		}
	}
	if keep < 0 {
		return subjects
	}
	out := make([]string, 0, len(subjects)-1)
	out = append(out, subjects[:keep]...)
	for _, s := range subjects[keep+1:] {
		if !IsJunkSubject(s) {
			out = append(out, s)
		}
	}
	return out
}
