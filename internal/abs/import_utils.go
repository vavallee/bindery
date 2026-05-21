package abs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

func authorExternalID(author NormalizedAuthor) string {
	if strings.TrimSpace(author.ID) != "" {
		return strings.TrimSpace(author.ID)
	}
	return "name:" + normalizeAuthorName(author.Name)
}

func seriesExternalID(series NormalizedSeries) string {
	if strings.TrimSpace(series.ID) != "" {
		return strings.TrimSpace(series.ID)
	}
	return "name:" + normalizeTitle(series.Name)
}

func absForeignID(kind, libraryID, externalID string) string {
	return fmt.Sprintf("abs:%s:%s:%s", kind, strings.TrimSpace(libraryID), strings.TrimSpace(externalID))
}

func parseABSDate(dateStr, yearStr string) *time.Time {
	dateStr = strings.TrimSpace(dateStr)
	if dateStr != "" {
		layouts := []string{time.RFC3339, "2006-01-02", "2006-1-2"}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, dateStr); err == nil {
				utc := parsed.UTC()
				return &utc
			}
		}
	}
	yearStr = strings.TrimSpace(yearStr)
	if yearStr != "" {
		if year, err := strconv.Atoi(yearStr); err == nil && year > 0 {
			t := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
			return &t
		}
	}
	return nil
}

func deriveMediaType(item NormalizedLibraryItem) string {
	hasAudio := len(item.AudioFiles) > 0
	hasEbook := strings.TrimSpace(item.EbookPath) != ""
	switch {
	case hasAudio && hasEbook:
		return models.MediaTypeBoth
	case hasEbook:
		return models.MediaTypeEbook
	case hasAudio:
		return models.MediaTypeAudiobook
	default:
		return models.MediaTypeAudiobook
	}
}

func mergeMediaType(current, next string) string {
	switch {
	case current == models.MediaTypeBoth || next == models.MediaTypeBoth:
		return models.MediaTypeBoth
	case current == "":
		return next
	case current == next:
		return current
	case (current == models.MediaTypeAudiobook && next == models.MediaTypeEbook) || (current == models.MediaTypeEbook && next == models.MediaTypeAudiobook):
		return models.MediaTypeBoth
	default:
		return current
	}
}

func deriveEditionFormats(item NormalizedLibraryItem) []string {
	formats := make([]string, 0, 2)
	if strings.TrimSpace(item.EbookPath) != "" {
		formats = append(formats, models.MediaTypeEbook)
	}
	if len(item.AudioFiles) > 0 {
		formats = append(formats, models.MediaTypeAudiobook)
	}
	if len(formats) == 0 {
		formats = append(formats, models.MediaTypeAudiobook)
	}
	return formats
}

func joinNarrators(narrators []string) string {
	return strings.Join(cleanStrings(narrators), ", ")
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func dedupeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeLibraryIDs(primary string, values []string) []string {
	primary = strings.TrimSpace(primary)
	out := make([]string, 0, len(values)+1)
	seen := make(map[string]struct{}, len(values)+1)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if primary != "" {
		if _, ok := seen[primary]; !ok {
			out = append([]string{primary}, out...)
		}
	}
	return out
}

func normalizeLanguage(language string) string {
	return strings.ToLower(strings.TrimSpace(language))
}

func normalizeAuthorName(name string) string {
	return textutil.NormalizeAuthorName(name)
}

// bracketSuffixRe matches one trailing square-bracketed qualifier. ABS titles
// routinely append format/edition tags this way ("[Unabridged]",
// "[Dramatized Adaptation]", "[Audiobook]"). indexer.NormalizeTitleForDedup
// only strips a trailing *parenthesised* qualifier, so without this step
// "The Eye of the World [Unabridged]" and "The Eye of the World" produce
// different keys and never match against the metadata provider or a local row.
var bracketSuffixRe = regexp.MustCompile(`\s*\[[^\[\]]*\]\s*$`)

// stripBracketSuffixes removes any trailing square-bracketed qualifiers,
// applied repeatedly so "Title [Unabridged] [2021]" is fully cleaned.
func stripBracketSuffixes(title string) string {
	for {
		stripped := bracketSuffixRe.ReplaceAllString(title, "")
		if stripped == title {
			return strings.TrimSpace(stripped)
		}
		title = stripped
	}
}

// normalizeTitle produces the canonical key used to compare an ABS item title
// against local book rows and metadata-provider works. ABS-specific bracketed
// edition/format noise is stripped first; the remainder reuses the shared
// indexer normalization (paren-suffix strip, subtitle strip, case/Unicode
// folding). The same wrapper is applied to both sides of every comparison, so
// the dedup key stays symmetric.
func normalizeTitle(title string) string {
	return indexer.NormalizeTitleForDedup(stripBracketSuffixes(strings.TrimSpace(title)))
}

func primaryAuthorName(item NormalizedLibraryItem) string {
	if len(item.Authors) == 0 {
		return ""
	}
	return strings.TrimSpace(item.Authors[0].Name)
}

func allowImmediateImport(item NormalizedLibraryItem) bool {
	return strings.TrimSpace(item.ASIN) != "" && (len(item.AudioFiles) > 0 || deriveMediaType(item) == models.MediaTypeBoth || deriveMediaType(item) == models.MediaTypeAudiobook)
}

func itemFileIDs(item NormalizedLibraryItem) []string {
	ids := make([]string, 0, len(item.AudioFiles)+1)
	for _, file := range item.AudioFiles {
		switch {
		case strings.TrimSpace(file.INO) != "":
			ids = append(ids, "audio:"+strings.TrimSpace(file.INO))
		case strings.TrimSpace(file.Path) != "":
			ids = append(ids, "audio:"+strings.TrimSpace(file.Path))
		}
	}
	if strings.TrimSpace(item.EbookINO) != "" {
		ids = append(ids, "ebook:"+strings.TrimSpace(item.EbookINO))
	} else if strings.TrimSpace(item.EbookPath) != "" {
		ids = append(ids, "ebook:"+strings.TrimSpace(item.EbookPath))
	}
	return ids
}

func sortNameFromFull(name string) string {
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return name
	}
	last := fields[len(fields)-1]
	rest := strings.Join(fields[:len(fields)-1], " ")
	return last + ", " + rest
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ptrString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func ptrInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func isbn13Ptr(raw string) *string {
	digits := isbnDigits(raw)
	if len(digits) == 13 {
		return &digits
	}
	return nil
}

func isbn10Ptr(raw string) *string {
	digits := isbnDigits(raw)
	if len(digits) == 10 {
		return &digits
	}
	return nil
}

func isbnDigits(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
