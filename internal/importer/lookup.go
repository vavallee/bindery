package importer

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// LookupResult is the outcome of a Lookup call.
type LookupResult struct {
	// Match is "confident" (single match), "ambiguous" (multiple matches), or
	// "none" (no match found in the local catalogue).
	Match          string        `json:"match"`
	Book           *models.Book  `json:"book,omitempty"`
	Candidates     []models.Book `json:"candidates,omitempty"`
	DetectedFormat string        `json:"detectedFormat"`
	ParsedTitle    string        `json:"parsedTitle"`
	ParsedAuthor   string        `json:"parsedAuthor"`
}

// Lookup parses a file or folder path, searches the local catalogue for a
// matching book, and returns the result. It does not modify any state.
//
// Search order:
//  1. ASIN exact match (if ASIN is present in the filename)
//  2. Title match (titleMatch) filtered by author when one is parsed
//
// Directories are treated as audiobook folders; their format is returned as
// "audiobook" regardless of content.
func (s *Scanner) Lookup(ctx context.Context, path string) (LookupResult, error) {
	parsed := ParseFilename(path)
	detectedFormat := lookupDetectFormat(path)

	result := LookupResult{
		DetectedFormat: detectedFormat,
		ParsedTitle:    parsed.Title,
		ParsedAuthor:   parsed.Author,
	}

	if parsed.Title == "" && parsed.ASIN == "" {
		result.Match = "none"
		return result, nil
	}

	books, err := s.books.List(ctx)
	if err != nil {
		return result, fmt.Errorf("lookup: list books: %w", err)
	}

	// Tier 1: ASIN exact match.
	if parsed.ASIN != "" {
		for i := range books {
			if books[i].ASIN == parsed.ASIN {
				result.Match = "confident"
				result.Book = &books[i]
				return result, nil
			}
		}
	}

	// Tiers 2+: title match with optional author filter.
	authors, err := s.authors.List(ctx)
	if err != nil {
		return result, fmt.Errorf("lookup: list authors: %w", err)
	}
	authorNames := make(map[int64]string, len(authors))
	for _, a := range authors {
		authorNames[a.ID] = a.Name
	}

	var matches []models.Book
	for _, b := range books {
		if !titleMatch(b.Title, parsed.Title) {
			continue
		}
		if parsed.Author != "" && !lookupAuthorMatch(parsed.Author, authorNames[b.AuthorID]) {
			continue
		}
		matches = append(matches, b)
	}

	switch len(matches) {
	case 0:
		result.Match = "none"
	case 1:
		result.Match = "confident"
		result.Book = &matches[0]
	default:
		result.Match = "ambiguous"
		result.Candidates = matches
	}
	return result, nil
}

// lookupDetectFormat returns "audiobook" for directory paths (multi-file
// audiobooks) and delegates to detectDownloadFormat for regular files.
func lookupDetectFormat(path string) string {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return models.MediaTypeAudiobook
	}
	return detectDownloadFormat([]string{path})
}

// lookupAuthorMatch returns true when parsed and catalogue author names refer
// to the same person. It handles comma-inverted forms ("Lane, Nick" vs
// "Nick Lane") and falls back to Jaro-Winkler similarity >= 0.80.
func lookupAuthorMatch(parsed, catalogue string) bool {
	pNorm := strings.ToLower(strings.TrimSpace(parsed))
	cNorm := strings.ToLower(strings.TrimSpace(catalogue))
	if pNorm == "" || cNorm == "" {
		return false
	}
	if pNorm == cNorm {
		return true
	}
	if invertAuthorName(pNorm) == cNorm {
		return true
	}
	if invertAuthorName(cNorm) == pNorm {
		return true
	}
	return textutil.JaroWinkler(pNorm, cNorm) >= 0.80
}

// invertAuthorName converts "Last, First" to "first last".
func invertAuthorName(name string) string {
	if i := strings.Index(name, ","); i > 0 {
		last := strings.TrimSpace(name[:i])
		first := strings.TrimSpace(name[i+1:])
		if first != "" {
			return first + " " + last
		}
	}
	return name
}
