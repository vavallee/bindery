package openlibrary

import (
	"regexp"
	"strings"
	"unicode"
)

// trailingGroupRE matches one bracketed or parenthesised group at the end of
// a title, capturing its contents: the " [Unabridged Audiobook]" of
// "Acts of War [Unabridged Audiobook]".
var trailingGroupRE = regexp.MustCompile(`\s*[\[(]([^\[\]()]*)[\])]\s*$`)

// trailingBareMarkerRE matches the two marker words written into a title with
// no brackets, after a space or a separator: "Acts of War: Unabridged
// Audiobook". Both words are required; a title that merely ends in
// "Audiobook" is left alone.
var trailingBareMarkerRE = regexp.MustCompile(`(?i)[\s,:;/-]+(?:unabridged\s*,?\s*audiobook|audiobook\s*,?\s*unabridged)\s*$`)

// formatMarkerWords are the only words a trailing group may hold to count as
// a format note rather than part of the title.
var formatMarkerWords = map[string]bool{"unabridged": true, "audiobook": true, "audio": true, "cd": true}

// stripFormatMarker removes the audio format note OpenLibrary records carry
// on the end of some work titles: "[Unabridged Audiobook]", "[Audiobook]",
// "(Audio CD)", and the bare "Unabridged Audiobook". The note describes one
// recording, not the work, and made the book's name read like a release.
//
// Trailing groups made only of the words unabridged, audiobook, audio and cd
// are peeled off, but the title changes only when one of them actually names
// the format (audiobook, or audio with cd). So "(Unabridged)" or "(CD)" alone
// stays, while "[Unabridged] [Audiobook]" goes together. A title that would
// be left empty is returned unchanged.
//
// Only the title changes. MediaType stays empty, so the add flow still applies
// the user's default format; see TestFormatMarkerLeavesMediaTypeAlone.
func stripFormatMarker(title string) string {
	original := strings.TrimSpace(title)
	s := original
	found := false
	for {
		m := trailingGroupRE.FindStringSubmatchIndex(s)
		if m == nil {
			break
		}
		words, marker := formatWords(s[m[2]:m[3]])
		if !words {
			break
		}
		found = found || marker
		s = strings.TrimSpace(s[:m[0]])
	}
	if loc := trailingBareMarkerRE.FindStringIndex(s); loc != nil {
		s = strings.TrimSpace(s[:loc[0]])
		found = true
	}
	if !found || s == "" {
		return original
	}
	return s
}

// formatWords reports whether text holds nothing but format marker words
// (words), and whether those words name an audio format (marker).
func formatWords(text string) (words, marker bool) {
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
	if len(tokens) == 0 {
		return false, false
	}
	seen := map[string]bool{}
	for _, t := range tokens {
		if !formatMarkerWords[t] {
			return false, false
		}
		seen[t] = true
	}
	return true, seen["audiobook"] || (seen["audio"] && seen["cd"])
}
