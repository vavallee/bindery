package textutil

import (
	"regexp"
	"strings"
)

// olDuplicateMarkerRE matches the note OpenLibrary's merge queue appends to a
// work it has flagged as a duplicate of another, as in
// "Light Bringer (Duplicate of OL31211W)". The note is an editor's
// bookkeeping, not part of the title, and only ever sits at the end.
var olDuplicateMarkerRE = regexp.MustCompile(`(?i)\s*\(\s*duplicate\s+of\s+(?:/works/)?OL\d+[AMW]\s*\)\s*$`)

// StripOpenLibraryDuplicateMarker removes a trailing "(Duplicate of OL...)"
// note from a title. A title that would be left empty is returned trimmed but
// otherwise unchanged, so a book never loses its only name.
func StripOpenLibraryDuplicateMarker(title string) string {
	trimmed := strings.TrimSpace(title)
	stripped := strings.TrimSpace(olDuplicateMarkerRE.ReplaceAllString(trimmed, ""))
	if stripped == "" {
		return trimmed
	}
	return stripped
}
