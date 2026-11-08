package textutil

import (
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
	for _, p := range gluedSubjectPrefixes {
		if len(s) <= len(p) || !strings.HasPrefix(lower, p) {
			continue
		}
		next := []rune(s[len(p):])[0]
		if next == ':' || next == '_' || unicode.IsUpper(next) || unicode.IsDigit(next) {
			return true
		}
	}
	return false
}

// DropJunkSubjects returns subjects without the entries IsJunkSubject
// rejects, preserving order. It returns the input slice unchanged when there
// is nothing to drop, and never returns nil for a non-nil input.
func DropJunkSubjects(subjects []string) []string {
	keep := -1
	for i, s := range subjects {
		if IsJunkSubject(s) {
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
