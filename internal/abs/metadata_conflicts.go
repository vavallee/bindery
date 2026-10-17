package abs

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

const (
	MetadataSourceABS      = "abs"
	MetadataSourceUpstream = "upstream"

	conflictStatusUnresolved = "unresolved"
	conflictStatusResolved   = "resolved"
)

var conflictFieldLabels = map[string]string{
	"description":    "Description",
	"image_url":      "Cover",
	"disambiguation": "Disambiguation",
	"sort_name":      "Sort name",
	"original_title": "Original title",
	"release_date":   "Release date",
	"language":       "Language",
	"average_rating": "Average rating",
	"ratings_count":  "Ratings count",
}

var authorConflictFields = []string{"description", "image_url", "disambiguation", "sort_name"}
var bookConflictFields = []string{"description", "image_url", "original_title", "release_date", "language", "average_rating", "ratings_count"}

func ConflictFieldLabel(field string) string {
	if label, ok := conflictFieldLabels[field]; ok {
		return label
	}
	return field
}

func SerializeAuthorConflictValue(author *models.Author, field string) string {
	if author == nil {
		return ""
	}
	switch field {
	case "description":
		return textutil.CleanDescription(author.Description)
	case "image_url":
		return strings.TrimSpace(author.ImageURL)
	case "disambiguation":
		return strings.TrimSpace(author.Disambiguation)
	case "sort_name":
		return strings.TrimSpace(author.SortName)
	default:
		return ""
	}
}

func SerializeBookConflictValue(book *models.Book, field string) string {
	if book == nil {
		return ""
	}
	switch field {
	case "description":
		return textutil.CleanDescription(book.Description)
	case "image_url":
		return strings.TrimSpace(book.ImageURL)
	case "original_title":
		return strings.TrimSpace(book.OriginalTitle)
	case "release_date":
		return formatConflictDate(book.ReleaseDate)
	case "language":
		return models.NormalizeLanguageCode(book.Language)
	case "average_rating":
		if book.AverageRating <= 0 {
			return ""
		}
		return strconv.FormatFloat(book.AverageRating, 'f', -1, 64)
	case "ratings_count":
		if book.RatingsCount <= 0 {
			return ""
		}
		return strconv.Itoa(book.RatingsCount)
	default:
		return ""
	}
}

func ApplyAuthorConflictValue(author *models.Author, field, value string) error {
	if author == nil {
		return fmt.Errorf("author is nil")
	}
	switch field {
	case "description":
		setUncreditedProfileField(&author.Description, &author.DescriptionSource, textutil.CleanDescription(value))
	case "image_url":
		setUncreditedProfileField(&author.ImageURL, &author.ImageSource, strings.TrimSpace(value))
	case "disambiguation":
		author.Disambiguation = strings.TrimSpace(value)
	case "sort_name":
		author.SortName = strings.TrimSpace(value)
	default:
		return fmt.Errorf("unsupported author conflict field %q", field)
	}
	return nil
}

// setUncreditedProfileField writes a conflict choice. Both sides of an author
// conflict are uncredited values (credited ones never become conflicts, see
// mergeCreditedAuthorField), so a changed value drops any credit the old one
// carried rather than keep one that no longer describes it (#3287).
func setUncreditedProfileField(cur *string, curSrc **models.ProfileSource, val string) {
	if val != *cur {
		*curSrc = nil
	}
	*cur = val
}

// mergeCreditedAuthorField handles a photo or bio field when the stored value
// or the upstream one carries a credit (#3287), with models.MergeProfileField.
// ok is false for any other field, or when neither side is credited, which
// leaves the field to conflict planning.
func mergeCreditedAuthorField(author, full *models.Author, field string) (changed, ok bool) {
	switch field {
	case "description":
		if author.DescriptionSource == nil && full.DescriptionSource == nil {
			return false, false
		}
		return models.MergeProfileField(&author.Description, &author.DescriptionSource, textutil.CleanDescription(full.Description), full.DescriptionSource), true
	case "image_url":
		if author.ImageSource == nil && full.ImageSource == nil {
			return false, false
		}
		return models.MergeProfileField(&author.ImageURL, &author.ImageSource, strings.TrimSpace(full.ImageURL), full.ImageSource), true
	}
	return false, false
}

func ApplyBookConflictValue(book *models.Book, field, value string) error {
	if book == nil {
		return fmt.Errorf("book is nil")
	}
	switch field {
	case "description":
		book.Description = textutil.CleanDescription(value)
	case "image_url":
		book.ImageURL = strings.TrimSpace(value)
	case "original_title":
		book.OriginalTitle = strings.TrimSpace(value)
	case "release_date":
		book.ReleaseDate = parseConflictDate(value)
	case "language":
		// A conflict recorded before #3086 holds the spelling its source
		// used ("en", "english"); what is written to the book is the same
		// ISO 639-2/B code every other path stores.
		book.Language = models.NormalizeLanguageCode(value)
	case "average_rating":
		book.AverageRating = parseConflictFloat(value)
	case "ratings_count":
		book.RatingsCount = parseConflictInt(value)
	default:
		return fmt.Errorf("unsupported book conflict field %q", field)
	}
	return nil
}

func normalizeConflictValue(field, value string) string {
	value = strings.TrimSpace(value)
	switch field {
	case "description":
		return textutil.CleanDescription(value)
	case "language":
		// "en" against "eng" is one language, not a conflict (#3086).
		return models.NormalizeLanguageCode(value)
	case "image_url":
		return strings.ToLower(value)
	default:
		return value
	}
}

func formatConflictDate(ts *time.Time) string {
	if ts == nil || ts.IsZero() {
		return ""
	}
	return ts.UTC().Format("2006-01-02")
}

func parseConflictDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func parseConflictFloat(value string) float64 {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parseConflictInt(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}
