package importer

import (
	"context"
	"fmt"
	"log/slog"
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

// matchBookForDownload tries to associate an unmatched download (one grabbed
// without a BookID, e.g. via the free-text Search page) with a catalogue book.
// It prefers embedded EPUB metadata (dc:title/dc:creator) over the release
// filename, since filenames encode author/title/series in inconsistent orders
// and routinely mis-parse (issue #1014). Returns the book and its author only
// on a single confident match; (nil, nil) when the catalogue has no
// unambiguous match, so the caller still surfaces the unmatched failure rather
// than importing against the wrong book.
func (s *Scanner) matchBookForDownload(ctx context.Context, files []string) (*models.Book, *models.Author) {
	// Tier 1: embedded EPUB metadata.
	for _, f := range files {
		if !IsEpubFile(f) {
			continue
		}
		meta, err := ReadEpubMetadata(f)
		if err != nil {
			slog.Debug("epub metadata unreadable; will try filename", "file", f, "error", err)
			continue
		}
		if b, a := s.matchByTitleAuthor(ctx, meta.Title, meta.Author); b != nil {
			slog.Info("matched download via embedded EPUB metadata",
				"file", f, "title", meta.Title, "author", meta.Author, "isbn", meta.ISBN, "bookID", b.ID)
			return b, a
		}
	}

	// Tier 2: release filename, via the same catalogue lookup manual import uses.
	for _, f := range files {
		res, err := s.Lookup(ctx, f)
		if err != nil {
			slog.Debug("filename lookup failed", "file", f, "error", err)
			continue
		}
		if res.Match == "confident" && res.Book != nil {
			a, err := s.authors.GetByID(ctx, res.Book.AuthorID)
			if err != nil {
				slog.Warn("matched book but failed to load author", "bookID", res.Book.ID, "error", err)
			}
			slog.Info("matched download via filename lookup",
				"file", f, "title", res.ParsedTitle, "author", res.ParsedAuthor, "bookID", res.Book.ID)
			return res.Book, a
		}
	}
	return nil, nil
}

// matchByTitleAuthor finds the single catalogue book whose title matches and
// (when an author is supplied) whose author matches. Mirrors Lookup's title
// tier but takes explicit title/author (e.g. from embedded metadata) instead of
// parsing a path. Returns (nil, nil) on zero or multiple matches — it never
// guesses between ambiguous candidates.
func (s *Scanner) matchByTitleAuthor(ctx context.Context, title, authorName string) (*models.Book, *models.Author) {
	if strings.TrimSpace(title) == "" {
		return nil, nil
	}
	books, err := s.books.List(ctx)
	if err != nil {
		return nil, nil
	}
	authors, err := s.authors.List(ctx)
	if err != nil {
		return nil, nil
	}
	names := make(map[int64]string, len(authors))
	for _, a := range authors {
		names[a.ID] = a.Name
	}
	var matches []models.Book
	for _, b := range books {
		if !titleMatch(b.Title, title) {
			continue
		}
		if authorName != "" && !lookupAuthorMatch(authorName, names[b.AuthorID]) {
			continue
		}
		matches = append(matches, b)
	}
	if len(matches) != 1 {
		return nil, nil
	}
	matched := matches[0]
	a, err := s.authors.GetByID(ctx, matched.AuthorID)
	if err != nil {
		slog.Warn("matched book but failed to load author", "bookID", matched.ID, "error", err)
	}
	return &matched, a
}

// unmatchedReason builds an actionable failure message for a download that
// matched no catalogue book, surfacing what was read from the file (parsed
// filename + embedded EPUB metadata) so the user can see WHY it didn't match
// rather than a bare "check the release title" (issue #1014 point 5).
func unmatchedReason(files []string) string {
	const base = "could not match this download to any book in your library"
	if len(files) == 0 {
		return base + " — no book files were found."
	}
	f := files[0]
	parsed := ParseFilename(f)
	var b strings.Builder
	fmt.Fprintf(&b, "%s. Parsed from filename: title=%q author=%q", base, parsed.Title, parsed.Author)
	if IsEpubFile(f) {
		if meta, err := ReadEpubMetadata(f); err == nil && (meta.Title != "" || meta.Author != "") {
			fmt.Fprintf(&b, "; embedded EPUB: title=%q author=%q", meta.Title, meta.Author)
			if meta.ISBN != "" {
				fmt.Fprintf(&b, " isbn=%s", meta.ISBN)
			}
		}
	}
	b.WriteString(". Add the matching book to your library, then retry the import.")
	return b.String()
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
